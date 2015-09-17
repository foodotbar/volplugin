package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/contiv/volplugin/config"
	"github.com/gorilla/mux"
)

type daemonConfig struct {
	config *config.TopLevelConfig
}

func daemon(config *config.TopLevelConfig, debug bool, listen string) {
	d := daemonConfig{config}
	r := mux.NewRouter()

	router := map[string]func(http.ResponseWriter, *http.Request){
		"/request": d.handleRequest,
		"/create":  d.handleCreate,
		"/mount":   d.handleMount,
		"/unmount": d.handleUnmount,
		"/remove":  d.handleRemove,
	}

	for path, f := range router {
		r.HandleFunc(path, logHandler(path, debug, f)).Methods("POST")
	}

	go scheduleSnapshotPrune(d.config)
	go scheduleSnapshots(d.config)

	if err := http.ListenAndServe(listen, r); err != nil {
		log.Fatalf("Error starting volmaster: %v", err)
	}

	select {}
}

func logHandler(name string, debug bool, actionFunc func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if debug {
			buf := new(bytes.Buffer)
			io.Copy(buf, r.Body)
			log.Debugf("Dispatching %s with %v", name, strings.TrimSpace(string(buf.Bytes())))
			var writer *io.PipeWriter
			r.Body, writer = io.Pipe()
			go func() {
				io.Copy(writer, buf)
				writer.Close()
			}()
		}

		actionFunc(w, r)
	}
}

func (d daemonConfig) handleRemove(w http.ResponseWriter, r *http.Request) {
	req, err := unmarshalRequest(r)
	if err != nil {
		httpError(w, "unmarshalling request", err)
		return
	}

	if err := removeImage(req.Pool, req.Volume); err != nil {
		httpError(w, "removing image", err)
		return
	}

	if err := d.config.RemoveVolume(req.Pool, req.Volume); err != nil {
		httpError(w, "clearing volume records", err)
		return
	}
}

func (d daemonConfig) handleUnmount(w http.ResponseWriter, r *http.Request) {
	req, err := unmarshalMountConfig(r)
	if err != nil {
		httpError(w, "Unmarshalling request", err)
		return
	}

	if err := d.config.RemoveMount(req); err != nil {
		httpError(w, "Could not publish mount information", err)
		return
	}
}

func (d daemonConfig) handleMount(w http.ResponseWriter, r *http.Request) {
	req, err := unmarshalMountConfig(r)
	if err != nil {
		httpError(w, "Unmarshalling request", err)
		return
	}

	if d.config.ExistsMount(req) {
		httpError(w, "Mount already exists", nil)
		return
	}

	if err := d.config.PublishMount(req); err != nil {
		httpError(w, "Could not publish mount information", err)
		return
	}
}

func (d daemonConfig) handleRequest(w http.ResponseWriter, r *http.Request) {
	req, err := unmarshalRequest(r)
	if err != nil {
		httpError(w, "Unmarshalling request", err)
		return
	}

	tenConfig, err := d.config.GetVolume(req.Pool, req.Volume)
	if err == nil {
		content, err := json.Marshal(tenConfig)
		if err != nil {
			httpError(w, "Marshalling response", err)
			return
		}

		w.Write(content)
		return
	}

	w.WriteHeader(404)
}

func (d daemonConfig) handleCreate(w http.ResponseWriter, r *http.Request) {
	content, err := ioutil.ReadAll(r.Body)
	if err != nil {
		httpError(w, "Reading request", err)
		return
	}

	var req config.RequestCreate

	if err := json.Unmarshal(content, &req); err != nil {
		httpError(w, "Unmarshalling request", err)
		return
	}

	if req.Tenant == "" {
		httpError(w, "Reading tenant", errors.New("tenant was blank"))
		return
	}

	if req.Pool == "" {
		httpError(w, "Reading tenant", errors.New("pool was blank"))
		return
	}

	if req.Volume == "" {
		httpError(w, "Reading tenant", errors.New("volume was blank"))
		return
	}

	tenConfig, err := d.config.CreateVolume(req.Volume, req.Tenant, req.Pool)
	if err != config.ErrExist && tenConfig != nil {
		if err := createImage(tenConfig, req.Pool, req.Volume); err != nil {
			httpError(w, "Creating volume", err)
			return
		}
	} else if err != nil && err != config.ErrExist {
		httpError(w, "Creating volume", err)
		return
	}

	content, err = json.Marshal(tenConfig)
	if err != nil {
		httpError(w, "Marshalling response", err)
		return
	}

	w.Write(content)
}
