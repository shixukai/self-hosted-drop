package app

import "net/http"

// A publication changes content, while resuming public access is a separate
// sites:manage operation. Check both states so a pending or failed pause cannot
// be overridden by a new publication.
func validatePublicationState(s Site, action string) error {
	switch action {
	case "PUBLISH", "ROLLBACK", "PAUSE", "RESUME":
		if s.Status == "DELETED" || s.DesiredStatus == "DELETED" {
			return apiError(http.StatusConflict, "SITE_DELETED", "请先恢复站点")
		}
	}
	if action == "PUBLISH" || action == "ROLLBACK" {
		if s.Status == "PAUSED" || s.DesiredStatus == "PAUSED" {
			return apiError(http.StatusConflict, "SITE_PAUSED", "站点已暂停，请先恢复服务再发布或回滚")
		}
	}
	return nil
}

func validatePublicationDeployment(d Deployment, reportHash string, acknowledgeWarnings bool) error {
	if d.Status != "READY" || d.ArtifactState != "AVAILABLE" {
		return apiError(http.StatusConflict, "ARTIFACT_UNAVAILABLE", "目标版本尚未就绪或已回收")
	}
	if reportHash != d.Report.ReportHash {
		return apiError(http.StatusConflict, "INSPECTION_CHANGED", "检查报告不匹配，请重新查看")
	}
	if len(d.Report.Warnings) > 0 && !acknowledgeWarnings {
		return apiError(http.StatusUnprocessableEntity, "WARNINGS_NOT_ACKNOWLEDGED", "请先确认检查警告")
	}
	return nil
}
