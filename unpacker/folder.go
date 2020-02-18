package unpacker

/* Folder Watching Codez */

import (
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"golift.io/xtractr"
)

// PollFolders begins the routines to watch folders for changes.
// if those changes include the addition of compressed files, they
// are processed for exctraction.
func (u *Unpackerr) PollFolders() {
	var flist []string

	var err error

	u.Folders, flist = u.checkFolders()

	time.Sleep(splay)

	u.folders, err = u.NewFolderWatcher()
	if err != nil {
		log.Println("[ERROR] Watching Folders:", err)
		return
	}
	// do not close the watcher.

	if len(u.Folders) == 0 {
		u.DeLogf("Folder: Nothing to watch, or no folders configured.")
		return
	}

	log.Println("[FOLDER] Watching:", strings.Join(flist, ", "))

	go u.folders.watchFSNotify()
}

// NewFolderWatcher returns a new folder watcher.
// You must call folders.Watcher.Close() when you're done with it.
func (u *Unpackerr) NewFolderWatcher() (*Folders, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for _, folder := range u.Folders {
		if err := watcher.Add(folder.Path); err != nil {
			log.Println("[ERROR] Folder (cannot watch):", err)
		}
	}

	return &Folders{
		Config:  u.Folders,
		Folders: make(map[string]*Folder),
		Events:  make(chan *eventData, queueChanSize),
		Updates: make(chan *update, updateChanSize),
		DeLogf:  u.DeLogf,
		Watcher: watcher,
	}, nil
}

// checkFolders stats all configured folders and returns only "good" ones.
func (u *Unpackerr) checkFolders() ([]*folderConfig, []string) {
	goodFolders := []*folderConfig{}
	goodFlist := []string{}

	for _, f := range u.Folders {
		if stat, err := os.Stat(f.Path); err != nil {
			log.Println("[ERROR] Folder (cannot watch):", err)
			continue
		} else if !stat.IsDir() {
			log.Printf("[ERROR] Folder (cannot watch): %s: not a folder", f.Path)
			continue
		}

		f.Path = strings.TrimSuffix(f.Path, "/") + "/"
		goodFolders = append(goodFolders, f)
		goodFlist = append(goodFlist, f.Path)
	}

	return goodFolders, goodFlist
}

func (u *Unpackerr) processFolderUpdate(update *update) {
	if _, ok := u.folders.Folders[update.Name]; !ok {
		// It doesn't exist? weird. delete it and bail out.
		delete(u.Map, update.Name)
		return
	}

	u.updateQueueStatus(&Extracts{Path: update.Name, Status: update.Step})
	u.folders.Folders[update.Name].last = time.Now()
	u.folders.Folders[update.Name].step = update.Step

	// Resp is only set when the extraction is finished.
	if update.Resp != nil {
		u.folders.Folders[update.Name].list = update.Resp.NewFiles
	}
}

// watchFSNotify reads file system events from a channel and processes them.
func (f *Folders) watchFSNotify() {
	for {
		select {
		case err, ok := <-f.Watcher.Errors:
			if !ok {
				return
			}

			log.Println("[ERROR] fsnotify:", err)
		case event, ok := <-f.Watcher.Events:
			if !ok {
				return
			}

			for _, cnfg := range f.Config {
				// Find the configured folder for the event we just got.
				if !strings.HasPrefix(event.Name, cnfg.Path) {
					continue
				}

				// cnfg.Path: "/Users/Documents/auto"
				// event.Name: "/Users/Documents/auto/my_folder/file.rar"
				// p: "my_folder"
				p := strings.TrimPrefix(event.Name, cnfg.Path)
				if np := path.Dir(p); np != "." {
					p = np
				}
				// Send this event to processEvent().
				f.Events <- &eventData{name: p, cnfg: cnfg, file: path.Base(event.Name)}
			}
		}
	}
}

// processEvent processes the event that was received.
func (f *Folders) processEvent(event *eventData) {
	fullPath := path.Join(event.cnfg.Path, event.name)
	if stat, err := os.Stat(fullPath); err != nil {
		// Item is unusable (probably deleted), remove it from history.
		if _, ok := f.Folders[fullPath]; ok {
			f.DeLogf("Removing Tracked Item: %v", fullPath)
			delete(f.Folders, fullPath)
			_ = f.Watcher.Remove(fullPath)
		}

		return
	} else if !stat.IsDir() {
		f.DeLogf("Ignoring Item: %v (not a folder)", fullPath)
		return
	}

	if _, ok := f.Folders[fullPath]; ok {
		//		f.DeLogf("Item Updated: %v (file: %v)", fullPath, event.file)
		f.Folders[fullPath].last = time.Now()
		return
	}

	if err := f.Watcher.Add(fullPath); err != nil {
		log.Printf("[ERROR] Tracking New Item: %v: %v", fullPath, err)
		return
	}

	log.Printf("[FOLDER] Tracking New Item: %v", fullPath)

	f.Folders[fullPath] = &Folder{
		last: time.Now(),
		step: DOWNLOADING,
		cnfg: event.cnfg,
	}
}

// xtractCallback is run twice by the xtractr library when the extraction begins, and finishes.
func (f *Folders) xtractCallback(resp *xtractr.Response) {
	switch {
	case !resp.Done:
		log.Printf("Extraction Started: %s, items in queue: %d", resp.X.Name, resp.Queued)
		f.Updates <- &update{Step: EXTRACTING, Name: resp.X.Name}
	case resp.Error != nil:
		log.Printf("Extraction Error: %s: %v", resp.X.Name, resp.Error)
		f.Updates <- &update{Step: EXTRACTFAILED, Name: resp.X.Name}
	default: // this runs in a go routine
		log.Printf("Extraction Finished: %s => elapsed: %d, archives: %d, extra archives: %d, files extracted: %d",
			resp.X.Name, resp.Elapsed, len(resp.Archives), len(resp.Extras), len(resp.AllFiles))
		// Send the update back into our channel (single go routine) to processFolderUpdate().
		f.Updates <- &update{Step: EXTRACTED, Resp: resp, Name: resp.X.Name}
	}
}
