package main

// Copyright 2017 Fardin Koochaki <fardin.koochaki@gmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import (
	"encoding/gob"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/go-plugins-helpers/volume"
)

const unionMountID = "_unionmount"

var (
	rootDir   string
	defaultFS string
)

func init() {
	defaultRoot := filepath.Join(volume.DefaultDockerRootDirectory, unionMountID)
	flag.StringVar(&rootDir, "root", defaultRoot, "Driver's root directory")

	dfs := "aufs"
	r, err := exec.Command("sh", "-c", "docker info --format '{{.Driver}}'").Output()
	if err != nil {
		fmt.Fprint(os.Stderr, "warning: cloud not get docker's storage driver\n")
		fmt.Fprintf(os.Stderr, "warning: %s", err)
	} else {
		dfs = strings.TrimSpace(string(r))
	}
	flag.StringVar(&defaultFS, "defaultfs", dfs, "Volumes' default filesystem")

	flag.Parse()
}

func main() {
	dfs, err := fsFromString(defaultFS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: unsupported driver (%s)\n", defaultFS)
	}

	d, err := loadState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s\n", err)
		d = newUnionMountDriver(rootDir, dfs)
	}

	h := volume.NewHandler(d)
	fmt.Println(h.ServeUnix("unionmount", 0))
}

func loadState() (d *unionMountDriver, _ error) {
	path := filepath.Join(rootDir, "state.gob")
	file, err := os.OpenFile(path, os.O_RDONLY, 0755)
	if err != nil {
		return d, err
	}
	defer file.Close()

	dec := gob.NewDecoder(file)
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("failed to decode '%s'", path)
	}

	return d, nil
}
