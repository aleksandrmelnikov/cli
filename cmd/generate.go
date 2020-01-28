/*
Copyright © 2019 NAME HERE <EMAIL ADDRESS>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cmd

import (
	"fmt"
	opConfig "github.com/onepanelio/cli/config"
	"github.com/onepanelio/cli/files"
	"github.com/onepanelio/cli/manifest"
	"github.com/onepanelio/cli/template"
	"github.com/onepanelio/cli/util"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// generateCmd represents the generate command
var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generates a kubernetes yaml configuration file.",
	Long: `Generates a kubernetes yaml configuration file given the 
OpDef file and parameters file, where you can customize components and overlays.

A sample usage is:

op-cli generate config.yaml params.yaml
`,
	Run: func(cmd *cobra.Command, args []string) {
		configFilePath := "config.yaml"

		if len(args) > 1 {
			configFilePath = args[0]
			return
		}

		config, err := opConfig.FromFile(configFilePath)
		if err != nil {
			fmt.Printf("Unable to read configuration file: %v", err.Error())
			return
		}

		kustomizeTemplate := TemplateFromSimpleOverlayedComponents(config.GetOverlayComponents())

		result, err := GenerateKustomizeResult(*config, kustomizeTemplate)
		if err != nil {
			log.Printf("Error generating result %v", err.Error())
			return
		}

		fmt.Printf("%v", result)
	},
}

func init() {
	rootCmd.AddCommand(generateCmd)

	// Here you will define your flags and configuration settings.
	//generateCmd.Flags().StringVarP(&configPath, "configPath", "c", "minikube", "Cloud provider to use. Example: AWS|GCP|Azure|Minikube")
	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// generateCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// generateCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

// Given the path to the manifests, and a kustomize config, creates the final kustomization file.
// It does this by copying the manifests into a temporary directory, inserting the kustomize template
// and running the kustomize command
func GenerateKustomizeResult(config opConfig.Config, kustomizeTemplate template.Kustomize) (string, error) {
	manifestPath := config.Spec.ManifestsRepo
	localManifestsCopyPath := ".manifests/cache"

	exists, err := files.Exists(localManifestsCopyPath)
	if err != nil {
		return "", err
	}

	if exists {
		if err := os.RemoveAll(localManifestsCopyPath); err != nil {
			return "", err
		}
	}

	if err := files.CopyDir(manifestPath, localManifestsCopyPath); err != nil {
		return "", err
	}

	localKustomizePath := filepath.Join(localManifestsCopyPath, "kustomization.yaml")
	if _, err := files.DeleteIfExists(localKustomizePath); err != nil {
		return "", err
	}

	newFile, err := os.Create(localKustomizePath)
	if err != nil {
		return "", err
	}

	kustomizeYaml, err := yaml.Marshal(kustomizeTemplate)
	if err != nil {
		log.Printf("Error yaml. Error %v", err.Error())
		return "", err
	}

	_, err = newFile.Write(kustomizeYaml)
	if err != nil {
		return "", err
	}



	yamlFile, err := util.LoadDynamicYaml(config.Spec.Params)
	if err != nil {
		return "", err
	}

	flatMap := yamlFile.Flatten(util.LowerCamelCaseFlatMapKeyFormatter)
	//Organize flatmap
	var keysAndValues map[string]string
	keysAndValues = make(map[string]string)
	for key := range flatMap {
		valueStr, ok := flatMap[key].(string)
		if !ok {
			valueBool, _ := flatMap[key].(bool)
			valueStr = "\"" + strconv.FormatBool(valueBool) + "\""
		}
		keysAndValues[key] = valueStr
	}
	//Read workflow-config-map-hidden for the rest of the values
	workflowEnvHiddenPath := filepath.Join(localManifestsCopyPath, "vars", "workflow-config-map-hidden.env")
	workflowEnvCont, workflowEnvFileErr := ioutil.ReadFile(workflowEnvHiddenPath)
	if workflowEnvFileErr != nil {
		return "", workflowEnvFileErr
	}
	workflowEnvContStr := string(workflowEnvCont)
	//Add these keys and values
	for _, line := range strings.Split(workflowEnvContStr,"\n") {
		keyValArr := strings.Split(line,"=")
		if len(keyValArr) != 2 {
			continue
		}
		keysAndValues[keyValArr[0]] = keyValArr[1]
	}

	//Write to env files
	//workflow-config-map.env
	if yamlFile.Get("artifactRepository.bucket") != nil &&
	yamlFile.Get("artifactRepository.endpoint") != nil &&
	yamlFile.Get("artifactRepository.insecure") != nil &&
	yamlFile.Get("artifactRepository.s3Region") != nil {
		//Clear previous env file
		paramsPath := filepath.Join(localManifestsCopyPath, "vars", "workflow-config-map.env")
		if _, err := files.DeleteIfExists(paramsPath); err != nil {
			return "", err
		}
		paramsFile, err := os.Create(paramsPath)
		if err != nil {
			return "", err
		}
		var stringToWrite = fmt.Sprintf("%v=%v\n%v=%v\n%v=%v\n%v=%v\n",
			"artifactRepositoryBucket",keysAndValues["artifactRepositoryBucket"],
			"artifactRepositoryEndpoint",keysAndValues["artifactRepositoryEndpoint"],
			"artifactRepositoryInsecure",keysAndValues["artifactRepositoryInsecure"],
			"artifactRepositoryS3Region",keysAndValues["artifactRepositoryS3Region"],
		)
		_, err = paramsFile.WriteString(stringToWrite)
		if err != nil {
			return "", err
		}
	} else {
		log.Fatal("Missing required values in params.yaml, artifactRepository. Check bucket, endpoint, or insecure.")
	}
	//logging-config-map.env
	if yamlFile.Get("logging.image") != nil &&
		yamlFile.Get("logging.volumeStorage") != nil {
		//Clear previous env file
		paramsPath := filepath.Join(localManifestsCopyPath, "vars", "logging-config-map.env")
		if _, err := files.DeleteIfExists(paramsPath); err != nil {
			return "", err
		}
		paramsFile, err := os.Create(paramsPath)
		if err != nil {
			return "", err
		}
		var stringToWrite = fmt.Sprintf("%v=%v\n%v=%v\n",
			"loggingImage",keysAndValues["loggingImage"],
			"loggingVolumeStorage",keysAndValues["loggingVolumeStorage"],
		)
		_, err = paramsFile.WriteString(stringToWrite)
		if err != nil {
			return "", err
		}
	} else {
		log.Fatal("Missing required values in params.yaml, logging. Check image, or volumeStorage.")
	}
	//onepanel-config-map.env
	if yamlFile.Get("defaultNamespace") != nil {
		//Clear previous env file
		paramsPath := filepath.Join(localManifestsCopyPath, "vars", "onepanel-config-map.env")
		if _, err := files.DeleteIfExists(paramsPath); err != nil {
			return "", err
		}
		paramsFile, err := os.Create(paramsPath)
		if err != nil {
			return "", err
		}
		var stringToWrite = fmt.Sprintf("%v=%v\n",
			"defaultNamespace",keysAndValues["defaultNamespace"],
		)
		_, err = paramsFile.WriteString(stringToWrite)
		if err != nil {
			return "", err
		}
	} else {
		log.Fatal("Missing required values in params.yaml, defaultNamespace")
	}
	//Write to secret files
	//common/onepanel/base/secrets.yaml
	var secretKeysValues []string
	if yamlFile.Get("artifactRepository.accessKeyValue") != nil &&
		yamlFile.Get("artifactRepository.secretKeyValue") != nil {
		secretKeysValues = append(secretKeysValues, "artifactRepositoryAccessKeyValue","artifactRepositorySecretKeyValue")
		for _, key := range secretKeysValues {
			//Path to secrets file
			secretsPath := filepath.Join(localManifestsCopyPath, "common","onepanel","base","secrets.yaml")
			//Read the file, replace the specific value, write the file back
			secretFileContent, secretFileOpenErr := ioutil.ReadFile(secretsPath)
			if secretFileOpenErr != nil {
				return "",secretFileOpenErr
			}
			secretFileContentStr := string(secretFileContent)
			value := flatMap[key]
			oldString := "$(" + key + ")"
			if strings.Contains(secretFileContentStr,key) {
				valueStr, ok := value.(string)
				if !ok {
					valueBool, _ := value.(bool)
					valueStr = "\"" + strconv.FormatBool(valueBool) + "\""
				}
				secretFileContentStr = strings.Replace(secretFileContentStr,oldString,valueStr,1)
				writeFileErr := ioutil.WriteFile(secretsPath,[]byte(secretFileContentStr),0644)
				if writeFileErr != nil {
					return "", writeFileErr
				}
			} else {
				fmt.Printf("Key: %v not present in %v, not used.\n",key,secretsPath)
			}
		}
	} else {
		log.Fatal("Missing required values in params.yaml, artifactRepository. Check accessKeyValue, or secretKeyValue.")
	}

	//To properly replace $(defaultNamespace), we need to update it in quite a few files.
	//Find those files
	listOfFiles, errorWalking := FilePathWalkDir(localManifestsCopyPath)
	if errorWalking != nil {
		return "", err
	}

	key := "defaultNamespace"
	valueStr := keysAndValues[key]
	for _, filePath := range listOfFiles {
		secretFileContent, secretFileOpenErr := ioutil.ReadFile(filePath)
		if secretFileOpenErr != nil {
			return "",secretFileOpenErr
		}
		secretFileContentStr := string(secretFileContent)
		//"defaultNamespace",keysAndValues["defaultNamespace"]
		oldString := "$(" + key + ")"
		if strings.Contains(secretFileContentStr,key) {
			secretFileContentStr = strings.Replace(secretFileContentStr,oldString,valueStr,-1)
			writeFileErr := ioutil.WriteFile(filePath,[]byte(secretFileContentStr),0644)
			if writeFileErr != nil {
				return "", writeFileErr
			}
		}
	}

	//Update the values in those files

	cmd := exec.Command("kustomize", "build", localManifestsCopyPath,  "--load_restrictor",  "none")
	stdOut, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	stdErr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	result, err := ioutil.ReadAll(stdOut)
	if err != nil {
		return "", err
	}

	errRes, err := ioutil.ReadAll(stdErr)
	if err != nil {
		log.Printf("Errors:\n%v", string(errRes))
		return "", err
	}

	if err := cmd.Wait(); err != nil {
		return "", err
	}

	return string(result), nil
}

func BuilderToTemplate(builder *manifest.Builder) template.Kustomize {
	k := template.Kustomize{
		ApiVersion: "kustomize.config.k8s.io/v1beta1",
		Kind: "Kustomization",
		Resources: make([]string, 0),
		Configurations: []string{"configs/varreference.yaml"},
	}

	for _, overlayComponent := range builder.GetOverlayComponents() {
		if !overlayComponent.HasOverlays() {
			k.Resources = append(k.Resources, overlayComponent.Component().Path())
			continue
		}

		for _, overlay := range overlayComponent.Overlays() {
			k.Resources = append(k.Resources, overlay.Path())
		}
	}

	return k
}

func TemplateFromSimpleOverlayedComponents(comps []*opConfig.SimpleOverlayedComponent) template.Kustomize {
	k := template.Kustomize{
		ApiVersion: "kustomize.config.k8s.io/v1beta1",
		Kind: "Kustomization",
		Resources: make([]string, 0),
		Configurations: []string{"configs/varreference.yaml"},
	}

	for _, overlayComponent := range comps {
		for _, item := range overlayComponent.PartsSkipFirst() {
			k.Resources = append(k.Resources, *item)
		}
	}

	return k
}

func FilePathWalkDir(root string) ([]string, error) {
	var filesFound []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			if !strings.Contains(path,".git") {
				filesFound = append(filesFound, path)
			}
		}
		return nil
	})
	return filesFound, err
}