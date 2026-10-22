// Package configuration reads configuration files.
//
// Like Buildbarn, Pigeon describes its configuration declaratively and
// hierarchically: each package that builds something from settings defines a
// configuration type for it, such as server.Configuration, and the
// application composes these into one top-level type that mirrors the
// configuration file. A file is a JSON object; fields that a type does not
// define are errors, so a misspelled setting does not go unnoticed.
package configuration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// LoadFile decodes the configuration file at path into cfg, which points to
// the top-level configuration type.
func LoadFile(path string, cfg any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := Decode(file, cfg); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Decode decodes one JSON configuration object from r into cfg.
func Decode(r io.Reader, cfg any) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cfg); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the configuration object")
	}
	return nil
}

// Duration is a time.Duration written as a string, such as "30s" or "1m30s".
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("a duration must be a string such as \"30s\": %s", data)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: write a number with a unit, such as \"30s\" or \"1m30s\"", value)
	}
	*d = Duration(parsed)
	return nil
}
