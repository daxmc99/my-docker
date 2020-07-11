package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const tempDir = "test"

var tag = "latest"

// Usage: your_docker.sh run <image> <command> <arg1> <arg2> ...
// Note: requires absolute path to work
func main() {
	image := os.Args[2]

	command := os.Args[3]
	args := os.Args[4:len(os.Args)]

	err := os.Mkdir(tempDir, 1755)
	if err != nil {
		if exist, ok := err.(*os.PathError); ok {
			if !strings.Contains(exist.Error(), "file exists") {
				panic(err)
			}
		}
	}

	//fmt.Println("fetching image:", image)
	err = fetchImage(image)
	if err != nil {
		panic(err)
	}

	// also copy binary to temp dir
	b, err := cp(command)
	if err != nil {
		panic(err)
	}

	_, err = Chroot(tempDir)
	if err != nil {
		panic(err)
	}
	//os.RemoveAll(tempDir + "/sh/")

	os.Chdir(tempDir)

	//fmt.Printf("About to run command: %s with args: %s \n", b, args)
	cmd := exec.Command(b, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// pid namespaces
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID,
	}

	err = cmd.Run()
	if err != nil {
		if syserr, ok := err.(*exec.ExitError); ok {
			os.Exit(syserr.ExitCode())
		}
		fmt.Printf("Err: %v", err)
		os.Exit(1)
	}
	// Print to both std-out and stderr
	//fmt.Fprint(os.Stdout, string(output))
	//fmt.Fprint(os.Stderr, string(output))

	// if err := exit(); err != nil {
	// 	panic(err)
	// }
	// TODO: we should should cleanup
	// os.Chdir("/app")
	// pwd()
	// ls()
	// if err := os.Remove("test"); err != nil {
	// 	panic(err)
	// }

}

func Chroot(path string) (func() error, error) {
	// grab a file descriptor
	root, err := os.Open("/")
	if err != nil {
		return nil, err
	}

	if err := syscall.Chroot(path); err != nil {
		root.Close()
		return nil, err
	}
	return func() error {
		defer root.Close()
		if err := root.Chdir(); err != nil {
			return err
		}
		return syscall.Chroot(".")
	}, nil
}

// func ls() {

// 	var files []string
// 	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
// 		files = append(files, path)
// 		return nil
// 	})
// 	if err != nil {
// 		panic(err)
// 	}
// 	for _, file := range files {
// 		fmt.Println(file)
// 	}
// }

// cp copies a file to the directory under /bin/ and returns a local path to the file
// ie /usr/bin/blah -> /dir/blah returning blah
func cp(file string) (string, error) {
	slic := strings.Split(file, "/")
	binName := slic[len(slic)-1]

	input, err := ioutil.ReadFile(file)
	if err != nil {
		return "", err
	}
	writeTo := tempDir + "/bin/" + binName
	//fmt.Println("writing file to: ", writeTo)
	//os.MkdirAll("/usr/local/bin/", 0777)
	err = ioutil.WriteFile(writeTo, input, 0777)
	if err != nil {
		return "", fmt.Errorf("couldn't create file: %v", err)
	}
	return binName, nil
}

// func pwd() {
// 	pwd, err := os.Getwd()
// 	if err != nil {
// 		panic(err)
// 	}
// 	fmt.Println("current dir is:", pwd)
// }

func fetchImage(image string) error {
	// all images are expected to be library images, ie busboxy, alpine, ubuntu
	image = "library/" + image
	resp, err := http.Get(
		fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull", image))

	if err != nil || resp.StatusCode != 200 {
		return fmt.Errorf("unable to fetch token: %v", err)
	}
	defer resp.Body.Close()
	/*
		export token=$(curl -s "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/alpine:pull" | jq -r .token)
	*/
	result := struct {
		Token string
	}{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return err
	}
	token := result.Token
	//fmt.Println("token is: ", token)

	/*
		curl -L -H "Authorization: Bearer $token" https://registry.hub.docker.com/v2/library/ubuntu/manifests/latest
	*/
	req, err := http.NewRequest("GET", fmt.Sprintf("https://registry.hub.docker.com/v2/%s/manifests/latest", image), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode > 200 {
		return fmt.Errorf("failed to get manifest: %v", err)
	}
	defer resp.Body.Close()

	var f ManifestResponse
	err = json.NewDecoder(resp.Body).Decode(&f)
	//fmt.Printf("%+v \n", f)
	if err != nil {
		return err
	}
	var filesNames []string
	for i, layer := range f.FsLayers {
		//fmt.Println("downloading layer: ", layer.BlobSum)
		path := fmt.Sprintf("https://registry.hub.docker.com/v2/%s/blobs/%s", image, layer.BlobSum)
		//fmt.Println(path)
		req, err := http.NewRequest("GET", path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}

		//fmt.Println("status code is:", resp.StatusCode)
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return fmt.Errorf("not a 200: %s", resp.Status)
		}
		index := strconv.Itoa(i)
		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		fileName := "/data" + index + ".tgz"
		filesNames = append(filesNames, fileName)
		err = ioutil.WriteFile(fileName, data, 0777)

		if err != nil {
			return err
		}

		//fmt.Println("wrote file: ", fileName)
	}

	for _, f := range filesNames {
		// fmt.Println(f)
		// h, err := os.Getwd()
		// if err != nil {
		// 	fmt.Println(err)
		// }
		// fmt.Println(h)
		// info, err := os.Stat(f)
		// if err != nil {
		// 	fmt.Println(err)
		// }
		// fmt.Println(info)

		//
		// info, err = os.Stat(actualTargetDir)
		// if err != nil {
		// 	fmt.Println(err)
		// }
		// fmt.Println(info)
		// fmt.Println(info.IsDir())

		// info, err = os.Stat("/bin/tar")
		// if err != nil {
		// 	fmt.Println(err)
		// }
		// fmt.Println(info)

		//cmd := exec.Cmd{Path: "/bin/tar", Args: []string{args}}
		actualTargetDir := "/app/test/"
		cmd := exec.Command("tar", "-zxf", f, "-C", actualTargetDir)
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		err = cmd.Run()
		if err != nil {
			return err
		}

	}
	return nil
}

type ManifestResponse struct {
	Name         string `json:"name"`
	Tag          string `json:"tag"`
	Architecture string `json:"architecture"`
	FsLayers     []struct {
		BlobSum string `json:"blobSum"`
	}
}
