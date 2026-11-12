package aischedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Collector checks, through the API of the collector and with the token of
// the run, what the technical account reads. Which account the schedules run
// as, and the privileges it holds, are the choice of the collector's
// administrators: an account of read groups only changes nothing whatever
// the tools of the agent.
type Collector struct {
	// URL is the base URL of the API, such as http://127.0.0.1:8081/api.
	URL    string
	Client *http.Client
}

// CheckSkill refuses a skill the account cannot read for the oc3 backend:
// deleted since the schedule was written, published to groups the account
// is not in, or for om3 only. The agent would run the prompt without it.
func (c Collector) CheckSkill(ctx context.Context, token, skill string) error {
	var meta struct {
		Backends []string `json:"backends"`
	}
	// No backend asked: the check is not counted as a read of the skill.
	err := c.get(ctx, token, "/ai/skills/"+url.PathEscape(skill), &meta)
	var status statusError
	if errors.As(err, &status) && status == http.StatusNotFound {
		return fmt.Errorf("the skill %s does not exist, or is not published to the account", skill)
	}
	if err != nil {
		return fmt.Errorf("skill %s: %w", skill, err)
	}
	if !slices.Contains(meta.Backends, "oc3") {
		return fmt.Errorf("the skill %s is not for the oc3 backend", skill)
	}
	return nil
}

type statusError int

func (s statusError) Error() string { return fmt.Sprintf("the collector answered %d", int(s)) }

func (c Collector) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
