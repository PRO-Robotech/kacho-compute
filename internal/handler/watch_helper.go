package handler

import (
	"strconv"

	commonv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/common/v1"
	"github.com/PRO-Robotech/kacho-corelib/watch"
)

func watchEventToProto(evt watch.Event) *commonv1.WatchEvent {
	var evtType commonv1.WatchEvent_EventType
	switch evt.EventType {
	case "ADDED":
		evtType = commonv1.WatchEvent_EVENT_TYPE_ADDED
	case "MODIFIED":
		evtType = commonv1.WatchEvent_EVENT_TYPE_MODIFIED
	case "DELETED":
		evtType = commonv1.WatchEvent_EVENT_TYPE_DELETED
	}
	return &commonv1.WatchEvent{
		EventType:       evtType,
		ResourceVersion: strconv.FormatInt(evt.ResourceVersion, 10),
		ResourceKind:    evt.ResourceKind,
		Data:            evt.Data,
	}
}
