// Package checkpoint defines the extension process object-output protocol.
package checkpoint

type Marker struct {
	Name string `json:"$object"`
}

type Draft struct {
	Type     string            `json:"type"`
	Metadata any               `json:"metadata"`
	Files    map[string]string `json:"files"`
}
