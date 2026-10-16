// Command multinodecheck verifies a running multi-node Strifira deployment
// end to end: cluster health on every node, stateless authentication (any
// node answers any request with the same token), cross-node realtime
// fan-out (an event created through one node reaches a websocket on
// another), replicated file storage (upload through one node, download
// through another) and the cluster-wide login throttle.
//
// Usage:
//
//	go run ./cmd/multinodecheck \
//	  -lb http://127.0.0.1:8080 \
//	  -node http://127.0.0.1:3301,http://127.0.0.1:3302,http://127.0.0.1:3303
//
// Exit code 0 means every check passed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
)

type check struct {
	name string
	fn   func(t *tool) error
}

var checks = []check{
	{"health: every node reports the full cluster", checkHealth},
	{"stateless: one token works on every node", checkStateless},
	{"realtime: event from node 2 reaches websocket on node 1", checkRealtime},
	{"files: upload on node 1, download on node 3", checkFiles},
	{"throttle: lockout spans the whole cluster", checkThrottle},
}

type tool struct {
	lb           string
	nodes        []string
	client       *http.Client
	token        string
	slug         string
	maxFailures  int
	failuresSeen int
}

func main() {
	lb := flag.String("lb", "http://127.0.0.1:8080", "load balancer base URL")
	nodes := flag.String("node", "http://127.0.0.1:3301,http://127.0.0.1:3302,http://127.0.0.1:3303", "comma-separated direct node base URLs")
	maxFailures := flag.Int("max-failures", 10, "login throttle maxFailures the server is configured with")
	flag.Parse()

	jar, _ := cookiejar.New(nil)
	t := &tool{
		lb:          strings.TrimRight(*lb, "/"),
		nodes:       strings.Split(*nodes, ","),
		client:      &http.Client{Timeout: 15 * time.Second, Jar: jar},
		maxFailures: *maxFailures,
	}
	for i := range t.nodes {
		t.nodes[i] = strings.TrimRight(strings.TrimSpace(t.nodes[i]), "/")
	}

	failed := 0
	for _, c := range checks {
		err := c.fn(t)
		if err != nil {
			failed++
			fmt.Printf("FAIL  %s: %v\n", c.name, err)
			continue
		}
		fmt.Printf("PASS  %s\n", c.name)
	}
	if failed > 0 {
		fmt.Printf("\n%d/%d checks failed\n", failed, len(checks))
		os.Exit(1)
	}
	fmt.Printf("\nall %d checks passed\n", len(checks))
}

// api performs one JSON request and decodes the response body when out is
// not nil.
func (t *tool) api(base, method, path, token string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := t.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if out != nil {
		data, err := io.ReadAll(res.Body)
		if err != nil {
			return res.StatusCode, err
		}
		if err := json.Unmarshal(data, out); err != nil {
			return res.StatusCode, fmt.Errorf("decoding %s: %w (%.120s)", path, err, data)
		}
	}
	return res.StatusCode, nil
}

func (t *tool) email(suffix string) string {
	return fmt.Sprintf("multinode-%s-%s@cluster.test", suffix, uuid.NewString()[:8])
}

func (t *tool) password() string { return "cluster-check-Password123!" }

// registerAndLogin creates a fresh user through the load balancer and saves
// the access token for the following checks.
func (t *tool) registerAndLogin(suffix string) error {
	email := t.email(suffix)
	status, err := t.api(t.lb, http.MethodPost, "/api/auth/register", "", map[string]string{
		"email": email, "password": t.password(), "name": "Multinode Check",
	}, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("register returned %d", status)
	}
	var login struct {
		AccessToken string `json:"access_token"`
	}
	status, err = t.api(t.lb, http.MethodPost, "/api/auth/login", "", map[string]string{
		"email": email, "password": t.password(),
	}, &login)
	if err != nil {
		return err
	}
	if status != http.StatusOK || login.AccessToken == "" {
		return fmt.Errorf("login returned %d", status)
	}
	t.token = login.AccessToken
	return nil
}

func checkHealth(t *tool) error {
	for _, node := range t.nodes {
		var health struct {
			Status  string `json:"status"`
			Cluster struct {
				Peers int `json:"peers"`
			} `json:"cluster"`
		}
		status, err := t.api(node, http.MethodGet, "/health", "", nil, &health)
		if err != nil {
			return fmt.Errorf("%s: %w", node, err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s: health returned %d", node, status)
		}
		if health.Cluster.Peers != len(t.nodes) {
			return fmt.Errorf("%s: cluster reports %d peers, want %d", node, health.Cluster.Peers, len(t.nodes))
		}
	}
	return nil
}

func checkStateless(t *tool) error {
	if err := t.registerAndLogin("stateless"); err != nil {
		return err
	}
	t.slug = "multinode-" + uuid.NewString()[:8]
	status, err := t.api(t.lb, http.MethodPost, "/api/workspaces", t.token, map[string]string{
		"name": "Multinode Check", "slug": t.slug,
	}, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("workspace create returned %d", status)
	}
	// The same token must work on every node, and the data written through
	// the balancer must be visible everywhere.
	for _, node := range t.nodes {
		var list struct {
			Items []struct {
				Slug string `json:"slug"`
			} `json:"items"`
		}
		status, err := t.api(node, http.MethodGet, "/api/workspaces", t.token, nil, &list)
		if err != nil {
			return fmt.Errorf("%s: %w", node, err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s: workspace list returned %d", node, status)
		}
		found := false
		for _, ws := range list.Items {
			if ws.Slug == t.slug {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s: workspace %s not visible", node, t.slug)
		}
	}
	return nil
}

func checkRealtime(t *tool) error {
	if t.slug == "" || t.token == "" {
		return fmt.Errorf("prerequisite check failed")
	}
	// A default team is required to create an issue.
	var team struct {
		ID string `json:"id"`
	}
	status, err := t.api(t.lb, http.MethodPost, "/api/workspaces/"+t.slug+"/teams", t.token, map[string]string{
		"name": "Cluster", "key": "CLU",
	}, &team)
	if err != nil {
		return err
	}
	if (status != http.StatusOK && status != http.StatusCreated) || team.ID == "" {
		return fmt.Errorf("team create returned %d", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Websocket on the first node; the event is created through the second.
	wsURL := strings.Replace(t.nodes[0], "http://", "ws://", 1) + "/api/workspaces/" + t.slug + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + t.token}},
	})
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	defer conn.Close(websocket.StatusGoingAway, "check done")

	created := make(chan string, 1)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var event struct {
				Type    string `json:"type"`
				Payload struct {
					Title string `json:"title"`
				} `json:"payload"`
			}
			if json.Unmarshal(data, &event) == nil && event.Type == "issue.created" {
				created <- event.Payload.Title
				return
			}
		}
	}()

	title := "cross-node-" + uuid.NewString()[:8]
	status, err = t.api(t.nodes[1], http.MethodPost, "/api/workspaces/"+t.slug+"/issues", t.token, map[string]any{
		"title": title, "team_id": team.ID,
	}, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("issue create on node 2 returned %d", status)
	}
	select {
	case got := <-created:
		if got != title {
			return fmt.Errorf("event received with title %q, want %q", got, title)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("no issue.created event arrived on node 1's websocket")
	}
}

func checkFiles(t *tool) error {
	if t.slug == "" || t.token == "" {
		return fmt.Errorf("prerequisite check failed")
	}
	content := []byte("jetstream replicated payload " + uuid.NewString())

	// Upload through the first node.
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("file", "cluster.txt")
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	form.Close()
	req, err := http.NewRequest(http.MethodPost, t.nodes[0]+"/api/workspaces/"+t.slug+"/upload", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+t.token)
	res, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var upload struct {
		URL string `json:"url"`
	}
	data, _ := io.ReadAll(res.Body)
	if (res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated) || json.Unmarshal(data, &upload) != nil || upload.URL == "" {
		return fmt.Errorf("upload on node 1 returned %d (%.120s)", res.StatusCode, data)
	}

	// Download the same object through the last node.
	dlReq, err := http.NewRequest(http.MethodGet, t.nodes[len(t.nodes)-1]+upload.URL, nil)
	if err != nil {
		return err
	}
	dlReq.Header.Set("Authorization", "Bearer "+t.token)
	dlRes, err := t.client.Do(dlReq)
	if err != nil {
		return err
	}
	defer dlRes.Body.Close()
	got, err := io.ReadAll(dlRes.Body)
	if err != nil {
		return err
	}
	if dlRes.StatusCode != http.StatusOK {
		return fmt.Errorf("download on node 3 returned %d", dlRes.StatusCode)
	}
	if !bytes.Equal(got, content) {
		return fmt.Errorf("downloaded bytes differ from the upload")
	}
	return nil
}

func checkThrottle(t *tool) error {
	// Burn the failure budget spread across all nodes, then the next
	// attempt — through the balancer, landing on any node — must be
	// refused cluster-wide.
	email := t.email("throttle")
	refused := 0
	for i := 0; i < t.maxFailures; i++ {
		node := t.nodes[i%len(t.nodes)]
		status, err := t.api(node, http.MethodPost, "/api/auth/login", "", map[string]string{
			"email": email, "password": "definitely-wrong-password",
		}, nil)
		if err != nil {
			return err
		}
		if status == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused > 0 {
		return fmt.Errorf("lockout fired %d attempts early (in-flight accounting over the budget)", refused)
	}
	status, err := t.api(t.lb, http.MethodPost, "/api/auth/login", "", map[string]string{
		"email": email, "password": "definitely-wrong-password",
	}, nil)
	if err != nil {
		return err
	}
	if status != http.StatusTooManyRequests {
		return fmt.Errorf("expected 429 on the attempt after the budget, got %d", status)
	}
	// Other accounts must be unaffected.
	if err := t.registerAndLogin("throttle-other"); err != nil {
		return fmt.Errorf("a different account could not log in after the lockout: %w", err)
	}
	return nil
}
