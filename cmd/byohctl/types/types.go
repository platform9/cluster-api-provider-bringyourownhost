package types

type TokenResponse struct {
    IDToken string `json:"id_token"`
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
    Kind          string                 `yaml:"kind"`
    Preferences   map[string]interface{} `yaml:"preferences"`
    Users         []struct {
        Name string `yaml:"name"`
        User struct {
            Token string `yaml:"token"`
        } `yaml:"user"`
    } `yaml:"users"`
}