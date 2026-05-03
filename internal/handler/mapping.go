package handler

import (
	"strconv"

	commonv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/common/v1"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

func protoSelectorsToService(selectors []*commonv1.Selector) []service.Selector {
	var result []service.Selector
	for _, s := range selectors {
		ss := service.Selector{}
		if fs := s.GetFieldSelector(); fs != nil {
			ss.Name = fs.GetName()
			ss.FolderID = fs.GetFolderId()
		}
		if ls := s.GetLabelSelector(); ls != nil {
			ss.Labels = ls
		}
		result = append(result, ss)
	}
	return result
}

func int64ToString(n int64) string {
	return strconv.FormatInt(n, 10)
}
