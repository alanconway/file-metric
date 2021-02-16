package main

import (
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	debugOn = flag.Bool("debug", false, "enable debug logging")
	address = flag.String("addr", ":2112", "listening addres")
)

func debug(f string, x ...interface{}) {
	if *debugOn {
		log.Printf(f, x...)
	}
}

type FileWatcher struct {
	watcher *fsnotify.Watcher
	metrics *prometheus.CounterVec
	sizes   map[string]float64
}

// FIXME(alanconway) supposed to be per input/output, not per file.
// Need to match what fluentd is reading? Or can we get fluentd to filter?

func NewFileWatcher(dir string) (*FileWatcher, error) {
	debug("watching %v", dir)
	w := &FileWatcher{
		metrics: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bytes_logged",
			Help: "total bytes logged to the given log file path",
		}, []string{"path"}),
		sizes: make(map[string]float64),
	}
	defer prometheus.Register(w.metrics)
	var err error
	if w.watcher, err = fsnotify.NewWatcher(); err == nil {
		err = w.watcher.Add(dir)
	}
	if err != nil {
		return nil, err
	}

	// Collect existing files, after starting watcher to avoid missing any.
	// It's OK if we update the same file twice.
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		err := w.Update(filepath.Join(dir, file.Name()))
		if err != nil {
			debug("error in update: %v", err)
		}
	}
	return w, nil
}

func (w *FileWatcher) Update(path string) error {
	counter, err := w.metrics.GetMetricWithLabelValues(path)
	if err != nil {
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return nil // Ignore directories
	}
	lastSize, size := float64(w.sizes[path]), float64(stat.Size())
	w.sizes[path] = size
	var add float64
	if size > lastSize {
		// File has grown, add the difference to the counter.
		add = size - lastSize
	} else if size < lastSize {
		// File truncated, starting over. Add the size.
		add = size
	}
	debug("%v: (%v->%v) +%v", path, lastSize, size, add)
	counter.Add(add)
	return nil
}

func (w FileWatcher) Watch() {
	for {
		var err error
		select {
		case e := <-w.watcher.Events:
			// FIXME(alanconway) better inotify wrapper? Register only for write events?
			debug("event %v", e)
			if e.Op == fsnotify.Write {
				// Write (which includes truncate) is the only operation that can change file size.
				w.Update(e.Name)
			}
			if err != nil && !os.IsNotExist(err) {
				debug("watch error: %v", err)
			}
		case err = <-w.watcher.Errors:
			if err != nil {
				debug("watch error: %v", err)
			}
		}
	}
}

func main() {
	flag.Parse()
	dir := flag.Arg(0)
	if dir == "" {
		dir = "."
	}
	w, err := NewFileWatcher(dir)
	if err != nil {
		log.Fatal(err)
	}
	go w.Watch()
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(*address, nil)
}
