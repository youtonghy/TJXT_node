package conf

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
)

// watchDebounce is how long the file must stay quiet before a reload; a
// variable so tests can shorten it.
var watchDebounce = 2 * time.Second

// Watcher reloads on changes of one config file until Close.
type Watcher struct {
	watcher *fsnotify.Watcher
	done    chan struct{}
	once    sync.Once
}

// Watch calls reload after the config file at filePath changed and still
// parses. It watches the parent directory, because editors and config
// management often replace the file by renaming a new one over it, which would
// silently end a watch on the file itself.
func Watch(filePath string, reload func()) (*Watcher, error) {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("new watcher error: %w", err)
	}
	if err := fw.Add(filepath.Dir(abs)); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("watch config directory error: %w", err)
	}
	w := &Watcher{watcher: fw, done: make(chan struct{})}
	go w.run(abs, reload)
	return w, nil
}

func (w *Watcher) run(path string, reload func()) {
	defer close(w.done)
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	fire := make(chan struct{}, 1)
	for {
		select {
		case e, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(e.Name) != path || e.Op == fsnotify.Chmod {
				continue
			}
			if timer == nil {
				timer = time.AfterFunc(watchDebounce, func() {
					select {
					case fire <- struct{}{}:
					default:
					}
				})
			} else {
				timer.Reset(watchDebounce)
			}
		case <-fire:
			// Parse into a fresh value: the running config is shared with the
			// core, and a broken file must not trigger a reload.
			if err := New().LoadFromPath(path); err != nil {
				log.WithField("err", err).Error("Config file changed but does not parse, keeping the running config")
				continue
			}
			log.Info("Config file changed, reloading")
			reload()
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.WithField("err", err).Error("Config file watcher error")
		}
	}
}

// Close stops watching and waits for the watch goroutine to exit.
func (w *Watcher) Close() error {
	var err error
	w.once.Do(func() {
		err = w.watcher.Close()
		<-w.done
	})
	return err
}
