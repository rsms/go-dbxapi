package dbxapi

import (
  "net/http"
  "encoding/json"
  "io"
  "io/ioutil"
  "bytes"
  "time"
  "errors"
  "path/filepath"
  "mime"
)

type Error string

func (e Error) Error() string { return string(e) }


type Client struct {
  AccessToken string
  client      http.Client
}

func NewClient(accessToken string) *Client {
  return &Client{accessToken, http.Client{}}
}

type Timestamp struct {
  // JSON format: "%Y-%m-%dT%H:%M:%SZ"
  time.Time
}

func (t *Timestamp) UnmarshalJSON(b []byte) (err error) {
  t.Time, err = time.Parse("2006-01-02T15:04:05Z", string(b[1 : len(b)-1]))
  return
}

func (t *Timestamp) MarshalJSON() ([]byte, error) {
  return []byte("\"" + t.Time.Format("2006-01-02T15:04:05Z") + "\""), nil
}

type Dimensions struct {
  Width  uint64 `json:"width"`
  Height uint64 `json:"height"`
}

type GpsCoordinates struct {
  Latitude  float64 `json:"latitude"`
  Longitude float64 `json:"longitude"`
}

type MediaMetadata struct {
  // Tags: "photo", "video"
  Tag string `json:".tag"`

  // Dimension of the photo/video. This field is optional.
  Dimensions *Dimensions `json:"dimensions"`

  // The GPS coordinate of the photo/video. This field is optional.
  Location *GpsCoordinates `json:"location"`

  // The timestamp when the photo/video is taken. This field is optional.
  TimeTaken Timestamp `json:"time_taken"`

  // The duration of the video in milliseconds. This field is optional.
  Duration uint64 `json:"duration"`
}

type MediaInfo struct {
  // Tags:
  //   "pending"  The photo/video is still under processing and metadata is not
  //              available yet. Metadata is nil in this case.
  //   "metadata" Metadata exists for the photo/video.
  Tag       string `json:".tag"`
  Metadata  *MediaMetadata `json:"metadata"`
}

type FolderEntry struct {
  // Tags: "file", "folder", "deleted"
  Tag  string `json:".tag"`

  // The last component of the path (including extension).
  // This never contains a slash.
  Name string `json:"name"`

  // A unique identifier for the file (min_length=1)
  Id string `json:"id"`

  // For files, this is the modification time set by the desktop client when the
  // file was added to Dropbox. Since this time is not verified (the Dropbox
  // server stores whatever the desktop client sends up), this should only be used
  // for display purposes (such as sorting) and not, for example, to determine if
  // a file has changed or not.
  ClientModified *Timestamp `json:"client_modified,omitempty"`

  // The last time the file was modified on Dropbox.
  ServerModified *Timestamp `json:"server_modified,omitempty"`

  // A unique identifier for the current revision of a file. This field is the
  // same rev as elsewhere in the API and can be used to detect changes and
  // avoid conflicts. String(min_length=9, pattern="[0-9a-f]+")
  Rev string `json:"rev"`

  // The file size in bytes.
  Size uint64 `json:"size"`

  // The lowercased full path in the user's Dropbox. Always starts with a slash.
  // This field will be null if the file or folder is not mounted.
  // This field is optional.
  PathLower string `json:"path_lower"`

  // The cased path to be used for display purposes only. In rare instances the
  // casing will not correctly match the user's filesystem, but this behavior
  // will match the path provided in the Core API v1. Changes to the casing of
  // paths won't be returned by list_folder/continue. This field will be null if
  // the file or folder is not mounted. This field is optional.
  PathDisplay string `json:"path_display"`

  // Additional information if the file is a photo or video.
  // This field is optional.
  MediaInfo *MediaInfo `json:"media_info"`
  
  // Set if this file is contained in a shared folder. This field is optional.
  //sharing_info FileSharingInfo?

  // Additional information if the file has custom properties with the property
  // template specified. This field is optional.
  //property_groups List of (PropertyGroup, )?

  // This flag will only be present if include_has_explicit_shared_members is
  // true in list_folder or get_metadata. If this flag is present, it will be
  // true if this file has any explicit shared members. This is different from
  // sharing_info in that this could be true in the case where a file has
  // explicit members but is not contained within a shared folder.
  // This field is optional.
  //has_explicit_shared_members Boolean?
}


var imageFileExts map[string]string


func init() {
  imageFileExts = map[string]string {
    ".jpg":  "jpg",
    ".jpeg": "jpg",
    ".png":  "png",
    ".gif":  "gif",
  }
}

// Returns a lower-case string e.g. "jpg" for image types.
// Returns the empty string for non-image types.
func (e *FolderEntry) ImageType() string {
  if e.Tag != "file" {
    return ""
  }
  ext := filepath.Ext(e.PathLower)
  if len(ext) == 0 {
    return ""
  }

  if e.MediaInfo != nil && e.MediaInfo.Tag == "metadata" {
    m := e.MediaInfo.Metadata
    if m != nil && m.Tag == "photo" {
      return ext[1:]
    }
  }
  // guess based on file extension
  ext1 := imageFileExts[ext]
  if len(ext1) > 0 {
    return ext1
  }

  t := mime.TypeByExtension(ext)
  if len(t) > 5 && t[0:5] == "image/" {
    return ext[1:]
  }

  return "";
}


type APIError struct {
  // Tags: "reset", "other"
  Tag string `json:".tag"`
}

type Result struct {
  Error *APIError `json:"error"`
  ErrorSummary string `json:"error_summary"`
}

type ListFolderResult struct {
  Result
  Entries []*FolderEntry `json:"entries"`
  Cursor  string `json:"cursor"`
  HasMore bool `json:"has_more"`
}

type ListFolderLongPollResult struct {
  // Indicates whether new changes are available. If true, call
  // list_folder/continue to retrieve the changes.
  Changes bool `json:"changes"`

  // If >0, backoff for at least this many seconds before calling
  // list_folder/longpoll again.
  Backoff uint64 `json:"backoff"`
}


const (
  apiURLPrefix    = "https://api.dropboxapi.com/2/"
  notifyURLPrefix = "https://notify.dropboxapi.com/2/"
  downloadURL     = "https://content.dropboxapi.com/2/files/download"
)


func (c *Client) checkRsp(rsp *http.Response, minst, maxst int) error {
  if rsp.StatusCode >= minst && rsp.StatusCode <= maxst {
    return nil
  }
  defer rsp.Body.Close()
  // if err == nil && res.Result.Error != nil {
  // }
  url := "?"
  if rsp.Request != nil && rsp.Request.URL != nil {
    url = rsp.Request.URL.String()
  }
  err := errors.New(url + ": " + rsp.Status)
  if rsp.StatusCode != 404 {
    body, _ := ioutil.ReadAll(rsp.Body)
    if body != nil && len(body) > 0 && len(body) < 200 {
      err = errors.New(url + ": " + string(body))
    }
  }
  return err
}


func (c *Client) RPC(url string, msg, res interface{}) error {
  b, err := json.Marshal(msg)
  if err != nil {
    return err
  }
  r, err := http.NewRequest("POST", url, bytes.NewBuffer(b))
  if len(url) > len(apiURLPrefix) && url[0:len(apiURLPrefix)] == apiURLPrefix {
    r.Header.Add("Authorization", "Bearer " + c.AccessToken)
  }
  r.Header.Add("Content-Type", "application/json")
  r.ContentLength = int64(len(b))
  rsp, err := c.client.Do(r)
  if err != nil {
    return err
  }

  if err := c.checkRsp(rsp, 200, 299); err != nil {
    return err
  }

  defer rsp.Body.Close()
  return json.NewDecoder(rsp.Body).Decode(&res);
  
  // body, err := ioutil.ReadAll(rsp.Body)
  // if err == nil {
  //   b := new(bytes.Buffer)
  //   if json.Indent(b, body, "", "  ") == nil {
  //     println("response:", b.String())
  //   }
  //   err = json.Unmarshal(body, res)
  // }
  // return err
}


// `identity` is interpreted depending on its prefix:
// - "id:"  file ID (e.g. "id:a4ayc_80_OEAAAAAAAAAYa")
// - "rev:" file rev (e.g. "rev:a1c10ce0dd78")
// - "/"    file path (e.g. "/Homework/math/Prime_Numbers.txt")
func (c *Client) Download(identity string) (io.ReadCloser, error) {
  idbuf, err := json.Marshal(identity)
  if err != nil {
    return nil, err
  }

  req, err := http.NewRequest("GET", downloadURL, nil)
  req.Header.Add("Authorization", "Bearer " + c.AccessToken)
  req.Header.Add("Dropbox-API-Arg", "{\"path\":" + string(idbuf) + "}")

  rsp, err := c.client.Do(req)
  if err != nil {
    return nil, err
  }

  if err := c.checkRsp(rsp, 200, 299); err != nil {
    return nil, err
  }

  return rsp.Body, nil
}


type ListFolderReq struct {
  Path                            string `json:"path"`
  Recursive                       bool `json:"recursive"`

  // Note: media info can't be used if you're planning to use ListFolderLongpollReq
  IncludeMediaInfo                bool `json:"include_media_info"`
  
  IncludeDeleted                  bool `json:"include_deleted"`
  IncludeHasExplicitSharedMembers bool `json:"include_has_explicit_shared_members"`
}

func (r ListFolderReq) Send(c Client) (*ListFolderResult, error) {
  var res ListFolderResult
  err := c.RPC(apiURLPrefix + "files/list_folder", r, &res)
  return &res, err
}


type ListFolderContReq struct {
  Cursor string `json:"cursor"`
}

func (r ListFolderContReq) Send(c Client) (*ListFolderResult, error) {
  var res ListFolderResult
  err := c.RPC(apiURLPrefix + "files/list_folder/continue", r, &res)
  return &res, err
}


type ListFolderLongpollReq struct {
  Cursor string `json:"cursor"`

  // A timeout in seconds. The request will block for at most this length of time,
  // plus up to 90 seconds of random jitter added to avoid the thundering herd
  // problem. Care should be taken when using this parameter, as some network
  // infrastructure does not support long timeouts.
  Timeout uint64 `json:"timeout"`
}

func (r ListFolderLongpollReq) Send(c Client) (*ListFolderLongPollResult, error) {
  var res ListFolderLongPollResult
  err := c.RPC(notifyURLPrefix + "files/list_folder/longpoll", r, &res)
  return &res, err
}
