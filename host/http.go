package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	tuf "github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-tuf/client"
	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/julienschmidt/httprouter"
	"github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/inconshreveable/log15.v2"
	"github.com/flynn/flynn/host/downloader"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/host/volume/api"
	"github.com/flynn/flynn/host/volume/manager"
	"github.com/flynn/flynn/pinkerton"
	"github.com/flynn/flynn/pinkerton/layer"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/sse"
	"github.com/flynn/flynn/pkg/version"
)

type Host struct {
	state   *State
	backend Backend
	vman    *volumemanager.Manager
	discMan *DiscoverdManager
	id      string
	url     string

	statusMtx sync.RWMutex
	status    *host.HostStatus

	discoverdOnce sync.Once
	networkOnce   sync.Once

	listener net.Listener

	maxJobConcurrency uint64

	log log15.Logger
}

var ErrNotFound = errors.New("host: unknown job")

func (h *Host) StopJob(id string) error {
	log := h.log.New("fn", "StopJob", "job.id", id)

	log.Info("acquiring state database")
	if err := h.state.Acquire(); err != nil {
		log.Error("error acquiring state database", "err", err)
		return err
	}
	defer h.state.Release()

	log.Info("getting job")
	job := h.state.GetJob(id)
	if job == nil {
		log.Warn("job not found")
		return ErrNotFound
	}
	switch job.Status {
	case host.StatusStarting:
		log.Info("job status is starting, marking it as stopped")
		h.state.SetForceStop(id)
		return nil
	case host.StatusRunning:
		log.Info("stopping job")
		return h.backend.Stop(id)
	default:
		log.Warn("job already stopped")
		return errors.New("host: job is already stopped")
	}
}

func (h *Host) SignalJob(id string, sig int) error {
	log := h.log.New("fn", "SignalJob", "job.id", id, "sig", sig)

	log.Info("getting job")
	job := h.state.GetJob(id)
	if job == nil {
		log.Warn("job not found")
		return ErrNotFound
	}
	log.Info("signalling job")
	return h.backend.Signal(id, sig)
}

func (h *Host) streamEvents(id string, w http.ResponseWriter) error {
	ch := h.state.AddListener(id)
	defer h.state.RemoveListener(id, ch)
	sse.ServeStream(w, ch, nil)
	return nil
}

type jobAPI struct {
	host                  *Host
	addJobRatelimitBucket chan struct{}
}

func (h *jobAPI) ListJobs(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if err := h.host.streamEvents("all", w); err != nil {
			httphelper.Error(w, err)
		}
		return
	}
	res := h.host.state.Get()

	httphelper.JSON(w, 200, res)
}

func (h *jobAPI) GetJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id := ps.ByName("id")
	log := h.host.log.New("fn", "GetJob", "job.id", id)

	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		log.Info("streaming job events")
		if err := h.host.streamEvents(id, w); err != nil {
			log.Error("error streaming job events", "err", err)
			httphelper.Error(w, err)
		}
		return
	}

	log.Info("getting job")
	job := h.host.state.GetJob(id)
	if job == nil {
		log.Warn("job not found")
		httphelper.ObjectNotFoundError(w, ErrNotFound.Error())
		return
	}
	httphelper.JSON(w, 200, job)
}

func (h *jobAPI) StopJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	id := ps.ByName("id")
	if err := h.host.StopJob(id); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *jobAPI) SignalJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	sig := ps.ByName("signal")
	if sig == "" {
		httphelper.ValidationError(w, "sig", "must not be empty")
		return
	}
	sigInt, err := strconv.Atoi(sig)
	if err != nil {
		httphelper.ValidationError(w, "sig", "must be an integer")
		return
	}
	id := ps.ByName("id")
	if err := h.host.SignalJob(id, sigInt); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *jobAPI) PullImages(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	log := h.host.log.New("fn", "PullImages")

	log.Info("extracting TUF database")
	tufDB, err := extractTufDB(r)
	if err != nil {
		log.Error("error extracting TUF database", "err", err)
		httphelper.Error(w, err)
		return
	}
	defer os.Remove(tufDB)

	info := make(chan layer.PullInfo)
	stream := sse.NewStream(w, info, nil)
	go stream.Serve()

	log.Info("pulling images")
	if err := pinkerton.PullImages(
		tufDB,
		r.URL.Query().Get("repository"),
		r.URL.Query().Get("driver"),
		r.URL.Query().Get("root"),
		r.URL.Query().Get("version"),
		info,
	); err != nil {
		log.Error("error pulling images", "err", err)
		stream.CloseWithError(err)
		return
	}

	stream.Wait()
}

func (h *jobAPI) PullBinariesAndConfig(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	log := h.host.log.New("fn", "PullBinariesAndConfig")

	log.Info("extracting TUF database")
	tufDB, err := extractTufDB(r)
	if err != nil {
		log.Error("error extracting TUF database", "err", err)
		httphelper.Error(w, err)
		return
	}
	defer os.Remove(tufDB)

	query := r.URL.Query()

	log.Info("creating local TUF store")
	local, err := tuf.FileLocalStore(tufDB)
	if err != nil {
		log.Error("error creating local TUF store", "err", err)
		httphelper.Error(w, err)
		return
	}
	opts := &tuf.HTTPRemoteOptions{
		UserAgent: fmt.Sprintf("flynn-host/%s %s-%s pull", version.String(), runtime.GOOS, runtime.GOARCH),
	}
	log.Info("creating remote TUF store")
	remote, err := tuf.HTTPRemoteStore(query.Get("repository"), opts)
	if err != nil {
		log.Error("error creating remote TUF store", "err", err)
		httphelper.Error(w, err)
		return
	}
	client := tuf.NewClient(local, remote)
	d := downloader.New(client, query.Get("version"))

	log.Info("downloading binaries")
	paths, err := d.DownloadBinaries(query.Get("bin-dir"))
	if err != nil {
		log.Error("error downloading binaries", "err", err)
		httphelper.Error(w, err)
		return
	}

	log.Info("downloading config")
	configs, err := d.DownloadConfig(query.Get("config-dir"))
	if err != nil {
		log.Error("error downloading config", "err", err)
		httphelper.Error(w, err)
		return
	}
	for k, v := range configs {
		paths[k] = v
	}

	httphelper.JSON(w, 200, paths)
}

func (h *jobAPI) AddJob(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// TODO(titanous): validate UUID
	id := ps.ByName("id")

	log := h.host.log.New("fn", "AddJob", "job.id", id)

	select {
	case h.addJobRatelimitBucket <- struct{}{}:
	default:
		log.Warn("maximum concurrent AddJob calls running")
		httphelper.Error(w, httphelper.JSONError{
			Code:    httphelper.RatelimitedErrorCode,
			Message: "maximum concurrent AddJob calls running, try again later",
			Retry:   true,
		})
		return
	}

	if shutdown.IsActive() {
		log.Warn("refusing to start job due to active shutdown")
		httphelper.JSON(w, 500, struct{}{})
		return
	}

	log.Info("decoding job")
	job := &host.Job{ID: id}
	if err := httphelper.DecodeJSON(r, job); err != nil {
		log.Error("error decoding job", "err", err)
		httphelper.Error(w, err)
		return
	}

	log.Info("acquiring state database")
	if err := h.host.state.Acquire(); err != nil {
		log.Error("error acquiring state database", "err", err)
		httphelper.Error(w, err)
		return
	}

	h.host.state.AddJob(job)

	go func() {
		log.Info("running job")
		err := h.host.backend.Run(job, nil)
		h.host.state.Release()
		if err != nil {
			log.Error("error running job", "err", err)
			h.host.state.SetStatusFailed(job.ID, err)
		}
		<-h.addJobRatelimitBucket
	}()

	// TODO(titanous): return 201 Accepted
	httphelper.JSON(w, 200, struct{}{})
}

func (h *jobAPI) ConfigureDiscoverd(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "ConfigureDiscoverd")

	log.Info("decoding config")
	var config host.DiscoverdConfig
	if err := httphelper.DecodeJSON(r, &config); err != nil {
		log.Error("error decoding config", "err", err)
		httphelper.Error(w, err)
		return
	}
	log.Info("config decoded", "url", config.URL, "dns", config.DNS)

	h.host.statusMtx.Lock()
	h.host.status.Discoverd = &config
	h.host.statusMtx.Unlock()

	if config.JobID != "" {
		log.Info("persisting discoverd job_id")
		if err := h.host.state.SetPersistentSlot("discoverd", config.JobID); err != nil {
			log.Error("error assigning discoverd to persistent slot")
		}
	}

	if config.URL != "" && config.DNS != "" {
		go h.host.discoverdOnce.Do(func() {
			log.Info("connecting to service discovery", "url", config.URL)
			if err := h.host.discMan.ConnectLocal(config.URL); err != nil {
				log.Error("error connecting to service discovery", "err", err)
				shutdown.Fatal(err)
			}
		})
	}
}

func (h *jobAPI) ConfigureNetworking(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "ConfigureNetworking")

	log.Info("decoding config")
	config := &host.NetworkConfig{}
	if err := httphelper.DecodeJSON(r, config); err != nil {
		log.Error("error decoding config", "err", err)
		shutdown.Fatal(err)
	}
	if config.JobID != "" {
		log.Info("persisting flannel job_id")
		if err := h.host.state.SetPersistentSlot("flannel", config.JobID); err != nil {
			log.Error("error assigning flannel to persistent slot")
		}
	}

	// configure the network before returning a response in case the
	// network coordinator requires the bridge to be created (e.g.
	// when using flannel with the "alloc" backend)
	h.host.networkOnce.Do(func() {
		log.Info("configuring network", "subnet", config.Subnet, "mtu", config.MTU, "resolvers", config.Resolvers)
		if err := h.host.backend.ConfigureNetworking(config); err != nil {
			log.Error("error configuring network", "err", err)
			shutdown.Fatal(err)
		}

		h.host.statusMtx.Lock()
		h.host.status.Network = config
		h.host.statusMtx.Unlock()
	})
}

func (h *jobAPI) GetStatus(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	h.host.statusMtx.RLock()
	defer h.host.statusMtx.RUnlock()
	httphelper.JSON(w, 200, &h.host.status)
}

func (h *jobAPI) UpdateTags(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var tags map[string]string
	if err := httphelper.DecodeJSON(r, &tags); err != nil {
		httphelper.Error(w, err)
		return
	}
	if err := h.host.UpdateTags(tags); err != nil {
		httphelper.Error(w, err)
		return
	}
	w.WriteHeader(200)
}

func (h *Host) UpdateTags(tags map[string]string) error {
	h.statusMtx.RLock()
	defer h.statusMtx.RUnlock()
	if err := h.discMan.UpdateTags(tags); err != nil {
		return err
	}
	h.status.Tags = tags
	return nil
}

func checkPort(port host.Port) bool {
	l, err := net.Listen(port.Proto, fmt.Sprintf(":%d", port.Port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func (h *jobAPI) ResourceCheck(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var req host.ResourceCheck
	if err := httphelper.DecodeJSON(r, &req); err != nil {
		httphelper.Error(w, err)
		return
	}
	var conflicts []host.Port
	for _, p := range req.Ports {
		if p.Proto == "" {
			p.Proto = "tcp"
		}
		if !checkPort(p) {
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		resp := host.ResourceCheck{Ports: conflicts}
		detail, err := json.Marshal(resp)
		if err != nil {
			httphelper.Error(w, err)
			return
		}
		httphelper.JSON(w, 409, &httphelper.JSONError{
			Code:    httphelper.ConflictErrorCode,
			Message: "Conflicting resources found",
			Detail:  detail,
		})
		return
	}
	httphelper.JSON(w, 200, struct{}{})
}

func (h *jobAPI) Update(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	log := h.host.log.New("fn", "Update")

	log.Info("decoding command")
	var cmd host.Command
	if err := httphelper.DecodeJSON(req, &cmd); err != nil {
		log.Error("error decoding command", "err", err)
		httphelper.Error(w, err)
		return
	}

	log.Info("updating host")
	err := h.host.Update(&cmd)
	if err != nil {
		httphelper.Error(w, err)
		return
	}

	// send an ok response and then shutdown after 1s to give the response
	// chance to reach the client.
	httphelper.JSON(w, http.StatusOK, cmd)
	log.Info("shutting down in 1s")
	time.AfterFunc(time.Second, func() {
		log.Info("exiting")
		os.Exit(0)
	})
}

func extractTufDB(r *http.Request) (string, error) {
	defer r.Body.Close()
	tmp, err := ioutil.TempFile("", "tuf-db")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, r.Body); err != nil {
		return "", err
	}
	return tmp.Name(), nil
}

func (h *jobAPI) RegisterRoutes(r *httprouter.Router) error {
	r.GET("/host/jobs", h.ListJobs)
	r.GET("/host/jobs/:id", h.GetJob)
	r.PUT("/host/jobs/:id", h.AddJob)
	r.DELETE("/host/jobs/:id", h.StopJob)
	r.PUT("/host/jobs/:id/signal/:signal", h.SignalJob)
	r.POST("/host/pull/images", h.PullImages)
	r.POST("/host/pull/binaries", h.PullBinariesAndConfig)
	r.POST("/host/discoverd", h.ConfigureDiscoverd)
	r.POST("/host/network", h.ConfigureNetworking)
	r.GET("/host/status", h.GetStatus)
	r.POST("/host/resource-check", h.ResourceCheck)
	r.POST("/host/update", h.Update)
	r.POST("/host/tags", h.UpdateTags)
	return nil
}

func (h *Host) ServeHTTP() {
	r := httprouter.New()

	r.POST("/attach", newAttachHandler(h.state, h.backend).ServeHTTP)

	jobAPI := &jobAPI{
		host: h,
		addJobRatelimitBucket: make(chan struct{}, h.maxJobConcurrency),
	}
	jobAPI.RegisterRoutes(r)

	volAPI := volumeapi.NewHTTPAPI(cluster.NewClient(), h.vman)
	volAPI.RegisterRoutes(r)

	go http.Serve(h.listener, httphelper.ContextInjector("host", httphelper.NewRequestLogger(r)))
}

func (h *Host) OpenDBs() error {
	if err := h.state.OpenDB(); err != nil {
		return err
	}
	return h.vman.OpenDB()
}

func (h *Host) CloseDBs() error {
	if err := h.state.CloseDB(); err != nil {
		return err
	}
	return h.vman.CloseDB()
}

func (h *Host) OpenLogs(buffers host.LogBuffers) error {
	return h.backend.OpenLogs(buffers)
}

func (h *Host) CloseLogs() (host.LogBuffers, error) {
	return h.backend.CloseLogs()
}

func (h *Host) Close() error {
	if h.listener != nil {
		return h.listener.Close()
	}
	return nil
}

func newHTTPListener(addr string) (net.Listener, error) {
	fdEnv := os.Getenv("FLYNN_HTTP_FD")
	if fdEnv == "" {
		return net.Listen("tcp", addr)
	}
	fd, err := strconv.Atoi(fdEnv)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "http")
	defer file.Close()
	return net.FileListener(file)
}
