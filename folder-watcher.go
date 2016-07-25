package dbxapi

import (
  "time"
  //"encoding/json"
)

const ErrCanceled = Error("canceled")

type DirMode int
const (
  DirModeShallow   = DirMode(iota)
  DirModeRecursive
)

type FolderWatcher struct {
  hasMore  bool
  exitch   chan error
  cancelch chan struct{}
  canceled bool
  changecb func(FolderChanges)

  Client
  Cursor        string
  Path          string
  DirMode
  EntriesById   map[string]*FolderEntry // key is file ID
  EntriesByPath map[string]*FolderEntry // key is lower-case path
  Exit          <-chan error
}

func NewFolderWatcher(client *Client, path string, m DirMode) *FolderWatcher {
  if client == nil {
    return nil
  }
  exitch := make(chan error)
  cancelch := make(chan struct{},1)
  w := FolderWatcher{
    exitch: exitch,
    cancelch: cancelch,
    Client: *client,
    Path: path,
    DirMode: m,
    EntriesById: make(map[string]*FolderEntry),
    EntriesByPath: make(map[string]*FolderEntry),
    Exit: exitch,
  }
  return &w
}

type FolderChanges struct {
  // values are keys into watcher.Entries map
  Added   []string
  Updated []string
  Removed []string
}

// Returns true if call caused w to be cancel. False is already canceled.
func (w *FolderWatcher) Cancel() {
  w.cancelch <- struct{}{}
}

func (w *FolderWatcher) checkCanceled() bool {
  if w.canceled {
    return true
  }
  select {
    case <-w.cancelch:
      w.canceled = true
      return true
    default:
      return false
  }
}

func (w *FolderWatcher) checkCanceledAndExit() bool {
  if w.checkCanceled() {
    w.exitch <- ErrCanceled
    return true
  }
  return false
}


func (w *FolderWatcher) checkResult(r *ListFolderResult, err error) bool {
  if err != nil {
    w.exitch <- err
    return false
  }
  if w.checkCanceledAndExit() {
    return false
  }

  // b, _ := json.MarshalIndent(r, "", "  ")
  // println("interpreted:", string(b))

  w.Cursor = r.Cursor
  w.hasMore = r.HasMore

  return true
}


// TODO: reset state when API response tells us to
// func (w *FolderWatcher) reset() bool {
//   w.Cursor = ""
//   w.EntriesById = make(map[string]*FolderEntry)
//   w.EntriesByPath = make(map[string]*FolderEntry)
// }


func (w *FolderWatcher) fetchInitial() bool {
  r, err := ListFolderReq{
    Path: w.Path,
    Recursive: w.DirMode == DirModeRecursive,
  }.Send(w.Client)
  if !w.checkResult(r, err) {
    return false
  }
  if len(r.Entries) > 0 {
    changes := FolderChanges{Added: make([]string, len(r.Entries))}
    for i, ent := range r.Entries {
      w.EntriesById[ent.Id] = ent
      w.EntriesByPath[ent.PathLower] = ent
      changes.Added[i] = ent.Id
    }
    w.changecb(changes)
  }
  return true
}


func (w *FolderWatcher) fetch() bool {
  if w.Cursor == "" {
    return w.fetchInitial()
  }

  r, err := ListFolderContReq{Cursor: w.Cursor}.Send(w.Client)
  if !w.checkResult(r, err) {
    return false
  }

  if len(r.Entries) == 0 {
    return true
  }

  changes := FolderChanges{}

  // Map of deleted entries, keyed by file id.
  // We coalesce "deleted" followed by "added"; for true deleted, fill delm.
  delm := make(map[string]string) // value=PathLower for truly deleted
  
  for _, ent := range r.Entries {
    prevEntAtPath := w.EntriesByPath[ent.PathLower]
    if ent.Tag == "deleted" {
      if prevEntAtPath != nil {
        delm[prevEntAtPath.Id] = ent.PathLower
      }
    } else {
      if prevEntAtPath != nil {
        // something exists at same path in our local state
        if prevEntAtPath.Id != ent.Id {
          // entry was removed and a different entry was added at the same path
          if w.EntriesById[prevEntAtPath.Id] != nil {
            delm[prevEntAtPath.Id] = ""
            changes.Updated = append(changes.Updated, ent.Id)
          } else {
            changes.Added = append(changes.Added, ent.Id)
          }
        } else {
          // Updated
          if prevEntAtPath != nil {
            delete(delm, ent.Id) // make sure entry is not marked as "deleted"
          }
          changes.Updated = append(changes.Updated, ent.Id)
        }
      } else {
        // Added
        if prevEntAtPath != nil {
          delete(delm, ent.Id) // make sure entry is not marked as "deleted"
        }
        changes.Added = append(changes.Added, ent.Id)
      }
      w.EntriesById[ent.Id] = ent
      w.EntriesByPath[ent.PathLower] = ent
    }
  }

  changes.Removed = make([]string, len(delm))
  i := 0
  for id, removedPath := range delm {
    delete(w.EntriesById, id)
    if len(removedPath) != 0 {
      delete(w.EntriesByPath, removedPath)
    }
    changes.Removed[i] = id
    i++
  }

  if len(changes.Added) > 0 ||
     len(changes.Updated) > 0 ||
     len(changes.Removed) > 0 {
    w.changecb(changes)
  }

  return true
}


func (w *FolderWatcher) waitForChanges() bool {
  for {
    r, err := ListFolderLongpollReq{Cursor: w.Cursor, Timeout: 30}.Send(w.Client)
    if err != nil {
      w.exitch <- err
      return false
    }
    if w.checkCanceledAndExit() {
      return false
    }
    if r.Backoff > 0 {
      time.Sleep(time.Duration(r.Backoff) * time.Second)
    }
    if r.Changes {
      w.hasMore = true
      return true
    }
  }
}


func (w *FolderWatcher) Run(changecb func(FolderChanges)) {
  w.changecb = changecb

  for {
    for {
      if !w.fetch() {
        return
      }
      if !w.hasMore {
        break
      }
    }
    if !w.waitForChanges() {
      return
    }
  }
}