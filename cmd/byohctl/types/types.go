// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package types

type TokenResponse struct {
	IDToken string `json:"id_token"`
}

// KeystoneDomain names a Keystone domain by ID or by name.
type KeystoneDomain struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// KeystoneAuthRequest is a Keystone v3 project-scoped password login.
type KeystoneAuthRequest struct {
	Auth struct {
		Identity struct {
			Methods  []string `json:"methods"`
			Password struct {
				User struct {
					Name     string         `json:"name"`
					Domain   KeystoneDomain `json:"domain"`
					Password string         `json:"password"`
				} `json:"user"`
			} `json:"password"`
		} `json:"identity"`
		Scope struct {
			Project struct {
				Name   string         `json:"name"`
				Domain KeystoneDomain `json:"domain"`
			} `json:"project"`
		} `json:"scope"`
	} `json:"auth"`
}

// KeystoneTokenResponse is the part of a Keystone v3 token response byohctl reads.
type KeystoneTokenResponse struct {
	Token struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	} `json:"token"`
}

type Secret struct {
	Data map[string]string `json:"data"`
}

type KubeConfig struct {
	APIVersion string `yaml:"apiVersion"`
	Clusters   []struct {
		Cluster struct {
			Server string `yaml:"server"`
		} `yaml:"cluster"`
		Name string `yaml:"name"`
	} `yaml:"clusters"`
	Contexts []struct {
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
		Name string `yaml:"name"`
	} `yaml:"contexts"`
	CurrentContext string                 `yaml:"current-context"`
	Kind           string                 `yaml:"kind"`
	Preferences    map[string]interface{} `yaml:"preferences"`
	Users          []struct {
		Name string `yaml:"name"`
		User struct {
			Token string `yaml:"token"`
		} `yaml:"user"`
	} `yaml:"users"`
}
