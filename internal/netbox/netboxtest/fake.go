// Package netboxtest is an in-memory imitation of the few NetBox REST
// endpoints the agent uses. It mirrors the response shapes of NetBox 4.x,
// including the 4.2 change that made MAC addresses separate objects.
package netboxtest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type Obj = map[string]any

type Server struct {
	*httptest.Server
	// Version is returned by /api/status/, e.g. "4.2.0" or "4.1.11".
	Version string
	// MaxPageSize caps "limit" like NetBox's MAX_PAGE_SIZE.
	MaxPageSize int
	// Fail makes every request whose path starts with it return 500.
	Fail string

	mu     sync.Mutex
	nextID int
	// objects by endpoint ("devices", "interfaces", ...) then by ID.
	objects map[string]map[int]Obj
	// Writes lists "METHOD /path" of every write, in order.
	Writes []string
}

func New(t *testing.T, version string) *Server {
	s := &Server{Version: version, MaxPageSize: 1000, nextID: 100, objects: map[string]map[int]Obj{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) macObjects() bool {
	var maj, min int
	_, _ = fmt.Sscanf(s.Version, "%d.%d", &maj, &min)
	return maj > 4 || (maj == 4 && min >= 2)
}

// Add stores an object and returns its ID. Use "device" (an ID) to attach
// components, "tags" ([]string slugs) for inventory items, and "mac_address"
// on interfaces.
func (s *Server) Add(endpoint string, o Obj) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(endpoint, o)
}

func (s *Server) add(endpoint string, o Obj) int {
	s.nextID++
	o["id"] = s.nextID
	if s.objects[endpoint] == nil {
		s.objects[endpoint] = map[int]Obj{}
	}
	s.objects[endpoint][s.nextID] = o
	if endpoint == "interfaces" && s.macObjects() {
		if mac, ok := o["mac_address"].(string); ok && mac != "" {
			delete(o, "mac_address")
			id := s.add("mac-addresses", Obj{"mac_address": mac, "assigned_object_id": o["id"]})
			o["primary_mac_address"] = id
		}
	}
	return s.nextID
}

// Objects returns copies of the stored objects of an endpoint, by ID.
func (s *Server) Objects(endpoint string) map[int]Obj {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int]Obj{}
	for id, o := range s.objects[endpoint] {
		out[id] = s.render(endpoint, o)
	}
	return out
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != "" && strings.HasPrefix(r.URL.Path, s.Fail) {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Token ") && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, `{"detail":"Authentication credentials were not provided."}`, http.StatusForbidden)
		return
	}
	if r.URL.Path == "/api/status/" {
		writeJSON(w, http.StatusOK, Obj{"netbox-version": s.Version})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api, app, endpoint, [id]
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	ep := parts[2]
	known := []string{"devices", "interfaces", "inventory-items", "tags", "ip-addresses"}
	if s.macObjects() {
		known = append(known, "mac-addresses")
	}
	if !slices.Contains(known, ep) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		s.Writes = append(s.Writes, r.Method+" "+r.URL.Path)
	}
	var body Obj
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if len(parts) == 3 {
		switch r.Method {
		case http.MethodGet:
			s.list(w, r, ep)
		case http.MethodPost:
			if err := s.validate(ep, nil, body); err != nil {
				writeJSON(w, http.StatusBadRequest, Obj{"detail": err.Error()})
				return
			}
			id := s.add(ep, body)
			writeJSON(w, http.StatusCreated, s.render(ep, s.objects[ep][id]))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	id, _ := strconv.Atoi(parts[3])
	o, ok := s.objects[ep][id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		if err := s.validate(ep, o, body); err != nil {
			writeJSON(w, http.StatusBadRequest, Obj{"detail": err.Error()})
			return
		}
		for k, v := range body {
			if k == "custom_fields" { // NetBox merges partial custom field updates
				maps.Copy(o["custom_fields"].(Obj), v.(Obj))
				continue
			}
			o[k] = v
		}
		writeJSON(w, http.StatusOK, s.render(ep, o))
	case http.MethodDelete:
		delete(s.objects[ep], id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// validate imitates the NetBox validation the agent could trip over.
func (s *Server) validate(ep string, existing, body Obj) error {
	switch ep {
	case "devices":
		if cf, ok := body["custom_fields"].(Obj); ok {
			defined, _ := existing["custom_fields"].(Obj)
			for k := range cf {
				if _, ok := defined[k]; !ok {
					return fmt.Errorf("unknown field name %q in custom field data", k)
				}
			}
		}
	case "interfaces":
		if s.macObjects() {
			if _, ok := body["mac_address"]; ok && existing == nil {
				// Writable again only from 4.7; the agent must not rely on it.
				return fmt.Errorf("mac_address is read-only")
			}
			if pm, ok := body["primary_mac_address"]; ok {
				m := s.objects["mac-addresses"][toInt(pm)]
				if m == nil || toInt(m["assigned_object_id"]) != toInt(existing["id"]) {
					return fmt.Errorf("MAC address is not assigned to this interface")
				}
			}
		}
	case "inventory-items":
		for _, k := range []string{"part_id", "serial"} {
			if v, _ := body[k].(string); len([]rune(v)) > 50 {
				return fmt.Errorf("%s: ensure this field has no more than 50 characters", k)
			}
		}
		if tags, ok := body["tags"].([]any); ok {
			var slugs []string
			for _, t := range tags {
				tag := s.objects["tags"][toInt(t)]
				if tag == nil {
					return fmt.Errorf("tag %v does not exist", t)
				}
				slugs = append(slugs, tag["slug"].(string))
			}
			body["tags"] = slugs
		}
	}
	return nil
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, ep string) {
	q := r.URL.Query()
	var all []Obj
	ids := make([]int, 0, len(s.objects[ep]))
	for id := range s.objects[ep] {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		o := s.objects[ep][id]
		if v := q.Get("device_id"); v != "" && strconv.Itoa(toInt(o["device"])) != v {
			continue
		}
		if v := q.Get("serial"); v != "" && !strings.EqualFold(fmt.Sprint(o["serial"]), v) {
			continue
		}
		if v := q.Get("slug"); v != "" && o["slug"] != v {
			continue
		}
		all = append(all, s.render(ep, o))
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > s.MaxPageSize {
		limit = s.MaxPageSize
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	page := []Obj{}
	if offset < len(all) {
		page = all[offset:min(offset+limit, len(all))]
	}
	writeJSON(w, http.StatusOK, Obj{"count": len(all), "next": nil, "previous": nil, "results": page})
}

// render builds the API representation of a stored object.
func (s *Server) render(ep string, o Obj) Obj {
	out := maps.Clone(o)
	if d, ok := o["device"]; ok {
		out["device"] = Obj{"id": toInt(d)}
	}
	switch ep {
	case "interfaces":
		if s.macObjects() {
			macs := []Obj{}
			out["mac_address"] = nil
			for _, m := range s.objects["mac-addresses"] {
				if toInt(m["assigned_object_id"]) == toInt(o["id"]) {
					macs = append(macs, Obj{"id": m["id"], "mac_address": m["mac_address"]})
					if toInt(o["primary_mac_address"]) == toInt(m["id"]) {
						out["mac_address"] = m["mac_address"]
						out["primary_mac_address"] = Obj{"id": m["id"], "mac_address": m["mac_address"]}
					}
				}
			}
			out["mac_addresses"] = macs
		}
	case "inventory-items":
		tags := []Obj{}
		for _, slug := range toStrings(o["tags"]) {
			for _, t := range s.objects["tags"] {
				if t["slug"] == slug {
					tags = append(tags, Obj{"id": t["id"], "slug": slug, "name": t["name"]})
				}
			}
		}
		out["tags"] = tags
		for _, k := range []string{"part_id", "serial", "description"} {
			if out[k] == nil {
				out[k] = ""
			}
		}
		if out["discovered"] == nil {
			out["discovered"] = false
		}
	}
	return out
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case Obj:
		return toInt(x["id"])
	}
	return 0
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
