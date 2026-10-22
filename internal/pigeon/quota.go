package pigeon

import (
	"net/http"
	"strconv"

	"github.com/royvandam/pigeon/pkg/quota"
)

func parseQuota(value string) (int64, error) {
	bytes, err := parseSize(value)
	if err != nil {
		return 0, err
	}
	return bytes, quota.Validate(bytes)
}

func quotaInput(quota int64) string {
	for _, unit := range []struct {
		bytes int64
		name  string
	}{{1 << 40, "TB"}, {1 << 30, "GB"}, {1 << 20, "MB"}, {1 << 10, "KB"}, {1, "B"}} {
		if quota%unit.bytes == 0 {
			return strconv.FormatInt(quota/unit.bytes, 10) + unit.name
		}
	}
	return ""
}

func setQuotaHeaders(w http.ResponseWriter, limit, used int64) {
	w.Header().Set("X-Quota-Limit", strconv.FormatInt(limit, 10))
	w.Header().Set("X-Quota-Used", strconv.FormatInt(used, 10))
	w.Header().Set("X-Quota-Remaining", strconv.FormatInt(quota.Remaining(limit, used), 10))
}
