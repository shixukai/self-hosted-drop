package app

import (
	"errors"
	"net/http"
	"testing"
)

func TestPublicationStatePolicy(t *testing.T) {
	states := []string{"DRAFT", "ACTIVE", "PAUSED", "DELETED"}
	for _, action := range []string{"PUBLISH", "ROLLBACK"} {
		for _, status := range states {
			for _, desired := range states {
				t.Run(action+"/"+status+"/"+desired, func(t *testing.T) {
					want := ""
					if status == "DELETED" || desired == "DELETED" {
						want = "SITE_DELETED"
					} else if status == "PAUSED" || desired == "PAUSED" {
						want = "SITE_PAUSED"
					}
					assertPublicationStateError(t, validatePublicationState(Site{Status: status, DesiredStatus: desired}, action), want)
				})
			}
		}
	}
}

func TestPublicationStateAllowsExplicitManagement(t *testing.T) {
	for _, action := range []string{"PAUSE", "RESUME", "DELETE", "RESTORE"} {
		t.Run(action+"/paused", func(t *testing.T) {
			assertPublicationStateError(t, validatePublicationState(Site{Status: "PAUSED", DesiredStatus: "PAUSED"}, action), "")
		})
	}
	for _, action := range []string{"PAUSE", "RESUME"} {
		for _, s := range []Site{{Status: "DELETED", DesiredStatus: "PAUSED"}, {Status: "PAUSED", DesiredStatus: "DELETED"}} {
			t.Run(action+"/"+s.Status+"/"+s.DesiredStatus, func(t *testing.T) {
				assertPublicationStateError(t, validatePublicationState(s, action), "SITE_DELETED")
			})
		}
	}
}

func assertPublicationStateError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected publication rejection: %v", err)
		}
		return
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != want || apiErr.Message == "" {
		t.Fatalf("got %v, want a 409 %s error with an explanation", err, want)
	}
}
