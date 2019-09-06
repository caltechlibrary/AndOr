//
// Package andor provides support for building simple digital
// object repositories in Go where objects are stored in a
// dataset collection and the UI of the repository is static
// HTML 5 documents using JavaScript to access a web API.
//
// @Author R. S. Doiel, <rsdoiel@library.caltech.edu>
//
package andor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"

	// Caltech Library Packages
	"github.com/caltechlibrary/dataset"
	"github.com/caltechlibrary/wsfn"
)

var (
	webService *wsfn.WebService
	mutex      = new(sync.Mutex)
)

// safeDatasetOp wraps Create, Update, Delete in a mutex
// to prevent corruption of items on disc like collection.json
func safeDatasetOp(c *dataset.Collection, key string, object map[string]interface{}, op int) error {
	mutex.Lock()
	defer mutex.Unlock()
	switch op {
	case CREATE:
		return c.Create(key, object)
	case UPDATE:
		return c.Update(key, object)
	case DELETE:
		return c.Delete(key)
	default:
		return fmt.Errorf("Unsupported operation type %d", op)
	}
}

// writeError
func writeError(w http.ResponseWriter, statusCode int) {
	http.Error(w, http.StatusText(statusCode), statusCode)
}

// writeJSON
func writeJSON(w http.ResponseWriter, r *http.Request, src []byte) {
	w.Header().Add("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(src); err != nil {
		log.Printf("Response write error, %s %s", r.URL.Path, err)
		return
	}
	log.Printf("FIXME: Log successful requests here ... %s", r.URL.Path)
}

func (s *AndOrService) requestAccessInfo(w http.ResponseWriter, r *http.Request) {
	// Who am I?
	username := s.getUsername(r)
	if username == "" {
		http.NotFound(w, r)
		return
	}
	log.Printf("DEBUG username %q", username)
	// What roles do I have?
	if user, ok := s.getUserInfo(username); ok == true {
		if roles, ok := s.getUserRoles(username); ok == true {
			o := map[string]interface{}{
				"user":  user,
				"roles": roles,
			}
			src, err := json.MarshalIndent(o, "", "    ")
			if err != nil {
				log.Printf("Failed to marshal %q, %s", username, err)
				writeError(w, http.StatusInternalServerError)
				return
			}
			// return payload appropriately
			writeJSON(w, r, src)
			return
		}
	}
	// Otherwise return 404, Not Found
	http.NotFound(w, r)
}

// requestKeys is the API version of `dataset keys COLLECTION_NAME`
// We only support GET on keys.
func (s *AndOrService) requestKeys(cName string, c *dataset.Collection, w http.ResponseWriter, r *http.Request) {
	var (
		keys []string
		err  error
	)
	keys = c.Keys()
	//NOTE: need to support filtered keys by object state and frame
	switch {
	case strings.Contains(r.URL.Path, "/keys/state/"):
		state := strings.TrimPrefix(r.URL.Path, "/"+cName+"/keys/state/")
		if state != "" {
			keys, err = c.KeyFilter(keys[:], fmt.Sprintf(`(eq ._State %q)`, strings.TrimSpace(state)))
		}
	}
	sort.Strings(keys)
	src, err := json.MarshalIndent(keys, "", "    ")
	if err != nil {
		log.Printf("Internal Server error, %s %s", cName, err)
		writeError(w, http.StatusInternalServerError)
		return
	}
	writeJSON(w, r, src)
}

// requestCreate is the API version of
//	`dataset create COLLECTION_NAME OBJECT_ID OBJECT_JSON`
func (s *AndOrService) requestCreate(cName string, c *dataset.Collection, w http.ResponseWriter, r *http.Request) {
	var (
		src []byte
		err error
	)
	// Make sure we have the right http Method
	if r.Method != "POST" {
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	// Make sure we can determine permissions before reading
	// post data.
	username := s.getUsername(r)
	if username == "" {
		writeError(w, http.StatusUnauthorized)
		return
	}
	roles, ok := s.getUserRoles(username)
	if ok == false {
		writeError(w, http.StatusUnauthorized)
		return
	}
	// We need to get the submitted object before checking
	// isAllowed.
	src, err = ioutil.ReadAll(r.Body)
	if err != nil {
		log.Printf("%s %s", r.URL.Path, err)
		writeError(w, http.StatusNotAcceptable)
		return
	}
	// We only accept content in JSON form with /create.
	object := make(map[string]interface{})
	decoder := json.NewDecoder(bytes.NewReader(src))
	decoder.UseNumber()
	if err = decoder.Decode(&object); err != nil {
		log.Printf("%s %s", r.URL.Path, err)
		writeError(w, http.StatusBadRequest)
		return
	}
	// Need to apply users/roles/states rules.
	state := getState(object)
	if s.isAllowed(roles, state, CREATE) == false {
		writeError(w, http.StatusUnauthorized)
		return
	}

	// Now get the proposed key.
	key := getKey(r.URL.Path, "/"+cName+"/create/")
	if c.HasKey(key) == true {
		log.Printf("%s, aborting create,  %q already exists in %s", username, key, c.Name)
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	// Need to make sure this part of the service is behind
	// the mutex.
	if err := safeDatasetOp(c, key, object, CREATE); err != nil {
		log.Printf("%s %s", r.URL.Path, err)
		writeError(w, http.StatusNotAcceptable)
		return
	}
	log.Printf("%s created %s in %s", username, key, c.Name)
}

// requestRead is the API version of
//     `dataset read -c -p COLLECTION_NAME KEY`
func (s *AndOrService) requestRead(cName string, c *dataset.Collection, w http.ResponseWriter, r *http.Request) {
	var (
		src []byte
		err error
	)
	username := s.getUsername(r)
	if username == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	roles, ok := s.getUserRoles(username)
	if ok == false {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	//FIXME: need to apply state filtering to keys requested
	keys := getKeys(r.URL.Path, "/"+cName+"/read/")
	if len(keys) == 0 {
		writeError(w, http.StatusBadRequest)
		return
	}
	unauthorized := false
	objects := []map[string]interface{}{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			object := make(map[string]interface{})
			if err = c.Read(strings.TrimSpace(key), object, false); err != nil {
				//FIXME: what do we do if one of a list of keys not found?
				log.Printf("Error reading key %q from %q, %s", key, c.Name, err)
			} else {
				state := getState(object)
				if s.isAllowed(roles, state, READ) {
					objects = append(objects, object)
				} else {
					unauthorized = true
					log.Printf("%q not allowed to read %q from %q", username, key, c.Name)
				}
			}
		}
	}
	switch len(objects) {
	case 0:
		if unauthorized {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "Not found", http.StatusNotFound)
		return
	case 1:
		src, err = json.MarshalIndent(objects[0], "", "    ")
	default:
		src, err = json.MarshalIndent(objects, "", "    ")
		if err != nil {
			log.Printf("Error reading key(s) %q from %q, %s", keys, cName, err)
			http.Error(w, "Internal Server error", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, r, src)
}

// requestUpdate is the API version of
//	`dataset update COLLECTION_NAME OBJECT_ID OBJECT_JSON`
func (s *AndOrService) requestUpdate(cName string, c *dataset.Collection, w http.ResponseWriter, r *http.Request) {
	// Make sure we have the right http Method
	if r.Method != "POST" {
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	// Make sure we can determine permissions before reading
	// post data.
	username := s.getUsername(r)
	if username == "" {
		writeError(w, http.StatusUnauthorized)
		return
	}
	roles, ok := s.getUserRoles(username)
	if ok == false {
		writeError(w, http.StatusUnauthorized)
		return
	}
	// We need to get the original object before proceeding with update.
	key := getKey(r.URL.Path, "/"+cName+"/update/")
	object := make(map[string]interface{})
	if err := c.Read(key, object, false); err != nil {
		log.Printf("%s %s", r.URL.Path, err)
		writeError(w, http.StatusNotFound)
		return
	}
	state := getState(object)
	if s.isAllowed(roles, state, UPDATE) {
		log.Printf("DEBUG state (original) %q for %s in %s -> %+v\n", state, key, cName, object)
		src, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Printf("%s %s", r.URL.Path, err)
			writeError(w, http.StatusNotAcceptable)
			return
		}
		// We only accept content in JSON form with /create.
		updatedObject := make(map[string]interface{})
		decoder := json.NewDecoder(bytes.NewReader(src))
		decoder.UseNumber()
		if err := decoder.Decode(&updatedObject); err != nil {
			log.Printf("%s %s", r.URL.Path, err)
			writeError(w, http.StatusUnsupportedMediaType)
			return
		}
		// NOTE: if Updated state is different we need to check
		// if we are allowed to change that state, otherwise
		// we need to preserve prior state!!!!
		if val, ok := updatedObject["_State"]; ok == true {
			newState := val.(string)
			if strings.Compare(state, newState) > 0 &&
				s.canAssign(roles, state, newState) == false {
				// we must preserve the old state.
				log.Printf("%s denied assigning from %q to %q for %s in %s", username, state, newState, key, c.Name)

				updatedObject["_State"] = state
			}
		} else {
			updatedObject["_State"] = state
		}

		log.Printf("DEBUG new (original) %q for %s in %s -> %+v\n", state, key, cName, updatedObject)
		// Need to make sure this part of the service is behind
		// the mutex.
		if err := safeDatasetOp(c, key, updatedObject, UPDATE); err != nil {
			log.Printf("%s %s", r.URL.Path, err)
			writeError(w, http.StatusNotAcceptable)
			return
		}
		log.Printf("%s updated %s in %s", username, key, c.Name)
	} else {
		log.Printf("%s denied update %s in %s", username, key, c.Name)
	}
}

// requestDelete is the API version of
//	`dataset Delete COLLECTION_NAME OBJECT_ID`
// except is doesn't actually delete the object. It changes the
// object's `._State` value.
func (s *AndOrService) requestDelete(cName string, c *dataset.Collection, w http.ResponseWriter, r *http.Request) {
	// Make sure we have the right http Method
	if r.Method != "POST" {
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	// Make sure we can determine permissions before reading
	// post data.
	username := s.getUsername(r)
	if username == "" {
		writeError(w, http.StatusUnauthorized)
		return
	}
	roles, ok := s.getUserRoles(username)
	if ok == false {
		writeError(w, http.StatusUnauthorized)
		return
	}
	key := getKey(r.URL.Path, "/"+cName+"/delete/")

	object := make(map[string]interface{})
	if err := c.Read(key, object, false); err != nil {
		writeError(w, http.StatusNotFound)
		return
	}
	state := getState(object)
	if s.isAllowed(roles, state, DELETE) {
		object["_State"] = "deleted"
		// Need to make sure this part of the service is behind
		// the mutex.
		if err := safeDatasetOp(c, key, object, UPDATE); err != nil {
			log.Printf("%s %s", r.URL.Path, err)
			writeError(w, http.StatusNotAcceptable)
			return
		}
	}
	log.Printf("%s deleted %s in %s", username, key, c.Name)
}

// requestAssignment retrieves an object, updates ._State and
// writes it back out.
func (s *AndOrService) requestAssignment(cName string, c *dataset.Collection, w http.ResponseWriter, r *http.Request) {
	// Make sure we have the right http Method
	if r.Method != "POST" {
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	// Make sure we can determine permissions before reading
	// post data.
	username := s.getUsername(r)
	if username == "" {
		writeError(w, http.StatusUnauthorized)
		return
	}
	roles, ok := s.getUserRoles(username)
	if ok == false {
		writeError(w, http.StatusUnauthorized)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"+cName+"/assign/"), "/")
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest)
		return
	}
	key, next := strings.TrimSpace(parts[0]), parts[1]
	object := make(map[string]interface{})
	if err := c.Read(key, object, false); err != nil {
		writeError(w, http.StatusNotFound)
		return
	}
	state := getState(object)
	if s.isAllowed(roles, state, ASSIGN) && s.canAssign(roles, state, next) {
		object["_State"] = next
		// Need to make sure this part of the service is behind
		// the mutex.
		if err := safeDatasetOp(c, key, object, UPDATE); err != nil {
			log.Printf("%s %s", r.URL.Path, err)
			writeError(w, http.StatusNotAcceptable)
			return
		}
	}
	log.Printf("%s assign %s to %s in %s", username, key, next, c.Name)
}

// addAccessRoute makes a route require an authentication mechanism,
// currently this is BasicAUTH but will likely become JWT.
func addAccessRoute(a *wsfn.Access, p string) {
	if a != nil {
		if a.Routes == nil {
			a.Routes = []string{}
		}
		a.Routes = append(a.Routes, p)
	}
}

// assignHandlers generates the /keys, /create, /read, /delete
// end points for accessing a collection in And/Or.
func (s *AndOrService) assignHandlers(mux *http.ServeMux, c *dataset.Collection) {
	cName := strings.TrimSuffix(c.Name, ".ds")
	access := s.Access
	//NOTE: We create a function handler based on on the
	// current collection being processed.
	log.Printf("Adding collection %q", c.Name)
	base := "/" + path.Base(cName)
	log.Printf("Adding access route %q", base)
	if s.IsAccessRestricted() {
		log.Printf("adding access policy to %q", base)
		addAccessRoute(access, base)
	}
	// End points based on dataset
	p := base + "/keys/"
	log.Printf("Adding handler %s", p)
	mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		s.requestKeys(cName, c, w, r)
	})
	// dataset object handling
	p = base + "/create/"
	log.Printf("Adding handler %s", p)
	mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		s.requestCreate(cName, c, w, r)
	})
	p = base + "/read/"
	log.Printf("Adding handler %s", p)
	mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		s.requestRead(cName, c, w, r)
	})
	p = base + "/update/"
	log.Printf("Adding handler %s", p)
	mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		s.requestUpdate(cName, c, w, r)
	})
	p = base + "/delete/"
	log.Printf("Adding handler %s", p)
	mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		s.requestDelete(cName, c, w, r)
	})

	p = base + "/assign/"
	log.Printf("Adding handler %s", p)
	mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		s.requestAssignment(cName, c, w, r)
	})

	// Additional And/Or specific end points
	p = "/" + path.Base(cName) + "/access/"
	log.Printf("Adding handler %s", p)
	mux.HandleFunc(p, s.requestAccessInfo)

	//FIXME: Need to add handler for working with attachments.
}

// RunService runs the http/https web service of AndOr.
func RunService(s *AndOrService) error {
	var (
		access *wsfn.Access
		cors   *wsfn.CORSPolicy
	)
	// Setup our web service from our *AndOrService
	u := new(url.URL)
	u.Scheme = s.Scheme
	u.Host = s.Host + ":" + s.Port
	if s.Access != nil {
		access = s.Access
	}
	if s.CORS != nil {
		cors = s.CORS
	}
	mux := http.NewServeMux()

	log.Printf("Have %d collection(s)", len(s.Collections))
	// NOTE: For each collection we assign our set of end points.
	for _, c := range s.Collections {
		s.assignHandlers(mux, c)
	}
	if s.Htdocs != "" {
		fs, err := wsfn.MakeSafeFileSystem(s.Htdocs)
		if err != nil {
			return err
		}
		mux.Handle("/", http.FileServer(fs))
	}
	hostname := fmt.Sprintf("%s:%s", u.Hostname(), u.Port())
	log.Printf("Starting service %s", hostname)
	switch s.Scheme {
	case "http":
		return http.ListenAndServe(hostname, wsfn.RequestLogger(cors.Handler(access.Handler(mux))))
	case "https":
		return http.ListenAndServeTLS(hostname, s.CertPEM, s.KeyPEM, wsfn.RequestLogger(cors.Handler(access.Handler(mux))))
	default:
		return fmt.Errorf("%q url scheme not supported", s.Scheme)
	}
}
