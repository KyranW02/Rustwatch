package app

import (
	"net/http"
	"time"
)

// featureRoutes adds the API for cameras, automations, server stats and raid costs.
func (a *App) featureRoutes(mux *http.ServeMux, post func(string, func(*http.Request) (any, error))) {
	type camReq struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		Buttons int32   `json:"buttons"`
		DX      float32 `json:"dx"`
		DY      float32 `json:"dy"`
	}
	post("/api/cameras/save", func(r *http.Request) (any, error) {
		var b camReq
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.SaveCamera(b.ID, b.Name)
	})
	post("/api/cameras/remove", func(r *http.Request) (any, error) {
		var b camReq
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.RemoveCamera(b.ID)
		return nil, nil
	})
	post("/api/cameras/open", func(r *http.Request) (any, error) {
		var b camReq
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.cams.Open(b.ID)
	})
	post("/api/cameras/close", func(r *http.Request) (any, error) {
		a.cams.Close()
		return nil, nil
	})
	post("/api/cameras/input", func(r *http.Request) (any, error) {
		var b camReq
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.cams.Input(b.Buttons, b.DX, b.DY)
	})
	mux.HandleFunc("GET /api/cameras/frame", func(w http.ResponseWriter, r *http.Request) {
		b, err := a.cams.PNG()
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})

	post("/api/automations/save", func(r *http.Request) (any, error) {
		var b Automation
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.SaveAutomation(b)
	})
	post("/api/automations/delete", func(r *http.Request) (any, error) {
		var b struct {
			ID string `json:"id"`
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		a.DeleteAutomation(b.ID)
		return nil, nil
	})
	post("/api/raidmode", func(r *http.Request) (any, error) {
		var b struct {
			On bool `json:"on"`
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		n, err := a.RaidMode(b.On)
		return map[string]int{"switched": n}, err
	})

	mux.HandleFunc("GET /api/mapfile", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.maps.Data()) })
	mux.HandleFunc("GET /api/mapfile/layer", func(w http.ResponseWriter, r *http.Request) {
		b, err := a.maps.Overlay(r.URL.Query().Get("name"))
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write(b)
	})
	post("/api/mapfile/rescan", func(r *http.Request) (any, error) {
		a.maps.Look(true)
		return nil, nil
	})
	post("/api/mapfile/rockname", func(r *http.Request) (any, error) {
		var b struct {
			Model string `json:"model"`
			Name  string `json:"name"`
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return nil, a.SetRockName(b.Model, b.Name)
	})
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.Stats()) })
	mux.HandleFunc("GET /api/raidcost", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"targets": raidTargets, "sulfur": raidSulfur, "asOf": time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC).Format("2 January 2006")})
	})
}
