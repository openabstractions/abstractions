package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentHelperUpdateFailureReportsRetainedRecoveryPath(t *testing.T) {
	for _, mode := range []string{"publication", "keep", "admission"} {
		t.Run(mode, func(t *testing.T) {
			const backup = "private/actual-unique-previous.exe"
			update := func(context.Context, func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
				if mode == "publication" {
					return nil, true, backup, errors.New("post-publication hold failed")
				}
				return &claudePanelFixtureImage{program: "current.exe"}, true, backup, nil
			}
			enroll := func(context.Context, claudePanelImage) (string, error) {
				if mode == "keep" {
					return "unknown", errors.New("Keep failed")
				}
				return "enrolled", nil
			}
			result, err := finishAgentHelperUpdate(context.Background(), update, nil, enroll, func(context.Context, claudePanelImage) (claudePanelImage, error) {
				return nil, errors.New("admission failed")
			})
			if err == nil || !strings.Contains(err.Error(), backup) || result.Outcome != "" || result.Enrollment != "" {
				t.Fatal(result, err)
			}
			response := httptest.NewRecorder()
			panelError(response, err)
			if response.Code != 503 || !strings.Contains(response.Body.String(), backup) {
				t.Fatal("recovery path lost at HTTP boundary", response.Code, response.Body.String())
			}
		})
	}
}

func TestAgentHelperUpdateReadyRequiresConfirmedEnrollment(t *testing.T) {
	for _, mode := range []string{"equal", "changed", "pending", "plugin-failure"} {
		t.Run(mode, func(t *testing.T) {
			called, enrolls := 0, 0
			image := &claudePanelFixtureImage{program: "helper.exe", key: "current-key"}
			update := func(context.Context, func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
				return image, mode != "equal", "previous.exe", nil
			}
			enroll := func(context.Context, claudePanelImage) (string, error) {
				enrolls++
				if mode == "pending" {
					return "pending", nil
				}
				return "enrolled", nil
			}
			admit := func(context.Context, claudePanelImage) (claudePanelImage, error) {
				return &claudePanelFixtureImage{key: image.FileKey()}, nil
			}
			result, err := finishAgentHelperUpdateWithReady(context.Background(), update, nil, enroll, admit, func(ctx context.Context, held claudePanelImage) error {
				called++
				if err := held.Check(ctx); err != nil {
					t.Fatal(err)
				}
				if mode == "plugin-failure" {
					return errors.New("plugin update unconfirmed")
				}
				return nil
			})
			want := 1
			if mode == "pending" {
				want = 0
			}
			if called != want || (mode == "equal" && enrolls != 0) {
				t.Fatal(called, enrolls)
			}
			if mode == "plugin-failure" {
				if err == nil || result.Outcome != "" || !strings.Contains(err.Error(), "previous.exe") {
					t.Fatal(result, err)
				}
			} else if err != nil {
				t.Fatal(result, err)
			}
		})
	}
}

func TestAgentHelperEqualPluginUpdateRequiresCurrentAdmissionWithoutKeep(t *testing.T) {
	for _, mode := range []string{"admitted", "refused", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			plain := &claudePanelFixtureImage{key: "same-key"}
			held := &claudePanelFixtureImage{key: plain.FileKey(), closed: mode == "revoked"}
			admits, effects := 0, 0
			update := func(context.Context, func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
				return plain, false, "", nil
			}
			enroll := func(context.Context, claudePanelImage) (string, error) {
				t.Fatal("equal bytes asked Keep")
				return "", nil
			}
			admit := func(context.Context, claudePanelImage) (claudePanelImage, error) {
				admits++
				if mode == "refused" {
					return nil, errors.New("enrollment refused")
				}
				return held, nil
			}
			result, err := finishAgentHelperUpdateWithReady(context.Background(), update, nil, enroll, admit, func(ctx context.Context, image claudePanelImage) error {
				effects++
				if image != held || image.Check(ctx) != nil {
					t.Fatal("plugin did not receive held enrollment")
				}
				return nil
			})
			if admits != 1 {
				t.Fatal(admits)
			}
			if mode == "admitted" {
				if err != nil || effects != 1 || result.Outcome != "current" || !held.closed {
					t.Fatal(result, effects, err)
				}
			} else if err == nil || effects != 0 || result.Outcome != "" {
				t.Fatal(result, effects, err)
			}
		})
	}
}

func TestAgentHelperUpdateRequiresOwnershipAndFreshKeep(t *testing.T) {
	for _, mode := range []string{"updated", "equal", "foreign", "pending", "refused", "wrong-image"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			enrolls, admits := 0, 0
			verify := func(context.Context, string, string) error {
				if mode == "foreign" {
					return errors.New("foreign")
				}
				return nil
			}
			update := func(ctx context.Context, check func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
				if err := check(ctx, "fixed-helper", "old-key"); err != nil {
					return nil, false, "", err
				}
				return &claudePanelFixtureImage{program: "fixed-helper", key: "new-key"}, mode != "equal", "previous-image", nil
			}
			enroll := func(context.Context, claudePanelImage) (string, error) {
				enrolls++
				if mode == "pending" {
					return "pending", nil
				}
				if mode == "refused" {
					return "unknown", errors.New("not kept")
				}
				return "enrolled", nil
			}
			admit := func(context.Context, claudePanelImage) (claudePanelImage, error) {
				admits++
				key := "new-key"
				if mode == "wrong-image" {
					key = "foreign"
				}
				return &claudePanelFixtureImage{program: "fixed-helper", key: key}, nil
			}
			result, err := finishAgentHelperUpdate(ctx, update, verify, enroll, admit)
			switch mode {
			case "equal":
				if err != nil || result.Outcome != "current" || enrolls != 0 || admits != 0 {
					t.Fatal(result, err, enrolls, admits)
				}
			case "updated":
				if err != nil || result.Outcome != "updated" || enrolls != 1 || admits != 1 {
					t.Fatal(result, err, enrolls, admits)
				}
			case "pending":
				if err != nil || result.Outcome != "permission_pending" || admits != 0 {
					t.Fatal(result, err)
				}
			default:
				if err == nil {
					t.Fatal("unproved update admitted", result)
				}
			}
		})
	}
}

func TestCodexHelperUpdateRejectsStaleUnownedAndCanceledIntent(t *testing.T) {
	for _, mode := range []string{"stale", "unowned", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			c := codexPanelFixture(t)
			c.current = codexConnectionView{Owned: mode != "unowned", Registration: "saved", Program: "fixed-helper"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			c.update = func(ctx context.Context, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
				if ctx.Err() != nil {
					return nil, false, "", ctx.Err()
				}
				t.Fatal("update reached without admitted intent")
				return nil, false, "", nil
			}
			revision := "1"
			if mode == "stale" {
				revision = "old"
			}
			if _, err := c.updateHelper(ctx, revision); err == nil {
				t.Fatal("unproved update")
			}
			c.close()
		})
	}
}
