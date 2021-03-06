package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model/event"
	"github.com/evergreen-ci/evergreen/model/user"
	"github.com/gorilla/mux"
)

func (uis *UIServer) fullEventLogs(w http.ResponseWriter, r *http.Request) {
	resourceType := strings.ToUpper(mux.Vars(r)["resource_type"])
	resourceId := mux.Vars(r)["resource_id"]
	u := GetUser(r)
	projCtx := MustHaveProjectContext(r)

	var eventQuery db.Q
	switch resourceType {
	case event.ResourceTypeTask:
		eventQuery = event.MostRecentTaskEvents(resourceId, 100)
	case event.ResourceTypeScheduler:
		eventQuery = event.RecentSchedulerEvents(resourceId, 500)
	case event.ResourceTypeHost:
		if u == nil {
			uis.RedirectToLogin(w, r)
			return
		}
		eventQuery = event.MostRecentHostEvents(resourceId, 5000)
	case event.ResourceTypeDistro:
		if u == nil {
			uis.RedirectToLogin(w, r)
			return
		}
		eventQuery = event.MostRecentDistroEvents(resourceId, 200)
	default:
		http.Error(w, fmt.Sprintf("Unknown resource: %v", resourceType), http.StatusBadRequest)
		return
	}

	loggedEvents, err := event.Find(event.AllLogCollection, eventQuery)
	if err != nil {
		uis.LoggedError(w, r, http.StatusInternalServerError, err)
		return
	}

	uis.WriteHTML(w, http.StatusOK, struct {
		ProjectData projectContext
		User        *user.DBUser
		Data        []event.Event
	}{projCtx, u, loggedEvents}, "base", "event_log.html", "base_angular.html")
}
