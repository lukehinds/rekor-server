package app

import (
	"net/http"
	"strings"

	"github.com/spf13/viper"
)

func IsFileTypeAllowed(byteLeaf []byte) (bool, error) {

	contentType := http.DetectContentType(byteLeaf)

	if viper.GetBool("rekor_server.enforce_text_only") == true {
		if !strings.Contains(contentType, "text/plain") {
			return false, nil

		} else {
			return true, nil
		}
	}
	return true, nil
}
