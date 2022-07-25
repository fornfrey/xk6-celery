package celery

import (
	"encoding/json"
)

type Serializer func(celeryBody []any) ([]byte, error)

type codec struct {
	ContentType string
	Encoding    string
	Serializer  Serializer
}

var codecs = map[string]*codec{
	"json": {
		ContentType: "application/json",
		Encoding:    "utf-8",
		Serializer:  jsonSerialize,
	},
}

func jsonSerialize(celeryBody []any) ([]byte, error) {
	return json.Marshal(celeryBody)
}

func RegisterSerializer(name string, serializer Serializer, contentType, encoding string) {
	codecs[name] = &codec{
		ContentType: contentType,
		Encoding:    encoding,
		Serializer:  serializer,
	}
}
