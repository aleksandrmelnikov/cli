package cmd

import (
	"encoding/base64"
	"errors"
	"fmt"
	v1 "github.com/onepanelio/core/pkg"
	"golang.org/x/crypto/bcrypt"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/util/rand"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	yaml2 "gopkg.in/yaml.v3"

	"sigs.k8s.io/kustomize/api/filesys"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/types"

	opConfig "github.com/onepanelio/cli/config"
	"github.com/onepanelio/cli/files"
	"github.com/onepanelio/cli/manifest"
	"github.com/onepanelio/cli/template"
	"github.com/onepanelio/cli/util"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

// generateCmd represents the generate command
var generateCmd = &cobra.Command{
	Use:   "build",
	Short: "Builds application YAML for preview.",
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

		kustomizeTemplate := TemplateFromSimpleOverlayedComponents(config.GetOverlayComponents(""))

		log.Printf("Building...")
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
	generateCmd.Flags().BoolVarP(&Dev, "dev", "", false, "Sets conditions to allow development testing.")
}

// Given the path to the manifests, and a kustomize config, creates the final kustomization file.
// It does this by copying the manifests into a temporary directory, inserting the kustomize template
// and running the kustomize command
func GenerateKustomizeResult(config opConfig.Config, kustomizeTemplate template.Kustomize) (string, error) {
	manifestPath := config.Spec.ManifestsRepo
	localManifestsCopyPath := ".onepanel/manifests/cache"

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

	yamlFile, err := util.LoadDynamicYamlFromFile(config.Spec.Params)
	if err != nil {
		return "", err
	}

	fqdn := yamlFile.GetValue("application.fqdn").Value
	cloudSettings, err := util.LoadDynamicYamlFromFile(config.Spec.ManifestsRepo + string(os.PathSeparator) + "vars" + string(os.PathSeparator) + "onepanel-config-map-hidden.env")
	if err != nil {
		return "", err
	}

	applicationApiPath := cloudSettings.GetValue("applicationCloudApiPath").Value
	applicationApiGrpcPort, _ := strconv.Atoi(cloudSettings.GetValue("applicationCloudApiGRPCPort").Value)
	applicationUiPath := cloudSettings.GetValue("applicationCloudUiPath").Value

	insecure, _ := strconv.ParseBool(yamlFile.GetValue("application.insecure").Value)
	httpScheme := "http://"
	wsScheme := "ws://"
	if !insecure {
		httpScheme = "https://"
		wsScheme = "wss://"
	}

	apiPath := httpScheme + fqdn + applicationApiPath
	uiApiPath := formatUrlForUi(apiPath)
	uiApiWsPath := formatUrlForUi(wsScheme + fqdn + applicationApiPath)

	yamlFile.PutWithSeparator("applicationApiUrl", uiApiPath, ".")
	yamlFile.PutWithSeparator("applicationApiWsUrl", uiApiWsPath, ".")
	yamlFile.PutWithSeparator("applicationApiPath", applicationApiPath, ".")
	yamlFile.PutWithSeparator("applicationUiPath", applicationUiPath, ".")
	yamlFile.PutWithSeparator("applicationApiGrpcPort", applicationApiGrpcPort, ".")
	yamlFile.PutWithSeparator("providerType", "cloud", ".")
	yamlFile.PutWithSeparator("onepanelApiUrl", apiPath, ".")

	coreImageTag := opConfig.CoreImageTag
	coreImagePullPolicy := "IfNotPresent"
	coreUiImageTag := opConfig.CoreUIImageTag
	coreUiImagePullPolicy := "IfNotPresent"
	if Dev {
		coreImageTag = "dev"
		coreImagePullPolicy = "Always"
		coreUiImageTag = "dev"
		coreUiImagePullPolicy = "Always"
	}
	yamlFile.PutWithSeparator("applicationCoreImageTag", coreImageTag, ".")
	yamlFile.PutWithSeparator("applicationCoreImagePullPolicy", coreImagePullPolicy, ".")

	yamlFile.PutWithSeparator("applicationCoreuiImageTag", coreUiImageTag, ".")
	yamlFile.PutWithSeparator("applicationCoreuiImagePullPolicy", coreUiImagePullPolicy, ".")

	applicationNodePoolOptionsConfigMapStr := generateApplicationNodePoolOptions(yamlFile.GetValue("application.nodePool").Content)
	yamlFile.PutWithSeparator("applicationNodePoolOptions", applicationNodePoolOptionsConfigMapStr, ".")

	provider := yamlFile.GetValue("application.provider").Value
	if provider == "minikube" || provider == "microk8s" {
		metalLbAddressesConfigMapStr := generateMetalLbAddresses(yamlFile.GetValue("metalLb.addresses").Content)
		yamlFile.PutWithSeparator("metalLbAddresses", metalLbAddressesConfigMapStr, ".")

		metalLbSecretKey, err := bcrypt.GenerateFromPassword([]byte(rand.String(128)), bcrypt.DefaultCost)
		if err != nil {
			return "", err
		}
		yamlFile.PutWithSeparator("metalLbSecretKey", base64.StdEncoding.EncodeToString(metalLbSecretKey), ".")
	}

	artifactRepoS3Node, _ := yamlFile.Get("artifactRepository.s3")
	if artifactRepoS3Node != nil {
		_, artifactRepoS3ParentNodeVal := yamlFile.Get("artifactRepository")
		artifactRepositoryConfig := v1.ArtifactRepositoryConfig{}

		err = artifactRepoS3ParentNodeVal.Decode(&artifactRepositoryConfig)
		if err != nil {
			return "", err
		}
		artifactRepositoryConfig.S3.AccessKeySecret.Key = artifactRepositoryConfig.S3.AccessKey
		artifactRepositoryConfig.S3.AccessKeySecret.Name = "$(artifactRepositoryS3AccessKeySecretName)"
		artifactRepositoryConfig.S3.SecretKeySecret.Key = artifactRepositoryConfig.S3.Secretkey
		artifactRepositoryConfig.S3.SecretKeySecret.Name = "$(artifactRepositoryS3SecretKeySecretName)"
		err, yamlStr := artifactRepositoryConfig.S3.MarshalToYaml()
		if err != nil {
			return "", err
		}
		yamlFile.Put("artifactRepositoryProvider", yamlStr)
	}
	artifactRepoGCSNode, _ := yamlFile.Get("artifactRepository.gcs")
	if artifactRepoGCSNode != nil {
		_, artifactRepoGCSParentNodeVal := yamlFile.Get("artifactRepository")
		artifactRepositoryConfig := v1.ArtifactRepositoryConfig{}

		err = artifactRepoGCSParentNodeVal.Decode(&artifactRepositoryConfig)
		if err != nil {
			return "", err
		}
		err, yamlConfigMap := artifactRepositoryConfig.GCS.MarshalToYaml()
		if err != nil {
			return "", err
		}

		yamlFile.Put("artifactRepositoryProvider", yamlConfigMap)
	}

	if artifactRepoS3Node == nil && artifactRepoGCSNode == nil {
		return "", errors.New("unsupported artifactRepository configuration")
	}
	flatMap := yamlFile.FlattenToKeyValue(util.LowerCamelCaseFlatMapKeyFormatter)

	//Read workflow-config-map-hidden for the rest of the values
	workflowEnvHiddenPath := filepath.Join(localManifestsCopyPath, "vars", "workflow-config-map-hidden.env")
	workflowEnvCont, workflowEnvFileErr := ioutil.ReadFile(workflowEnvHiddenPath)
	if workflowEnvFileErr != nil {
		return "", workflowEnvFileErr
	}
	workflowEnvContStr := string(workflowEnvCont)
	//Add these keys and values
	for _, line := range strings.Split(workflowEnvContStr, "\n") {
		keyValArr := strings.Split(line, "=")
		if len(keyValArr) != 2 {
			continue
		}
		flatMap[keyValArr[0]] = keyValArr[1]
	}

	//Write to env files
	//workflow-config-map.env
	if yamlFile.HasKey("artifactRepository.s3") {
		if yamlFile.HasKeys("artifactRepository.s3.bucket", "artifactRepository.s3.endpoint", "artifactRepository.s3.insecure", "artifactRepository.s3.region") {
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
				"artifactRepositoryBucket", flatMap["artifactRepositoryS3Bucket"],
				"artifactRepositoryEndpoint", flatMap["artifactRepositoryS3Endpoint"],
				"artifactRepositoryInsecure", flatMap["artifactRepositoryIS3nsecure"],
				"artifactRepositoryRegion", flatMap["artifactRepositoryS3Region"],
			)
			_, err = paramsFile.WriteString(stringToWrite)
			if err != nil {
				return "", err
			}
		} else {
			log.Fatal("Missing required values in params.yaml, artifactRepository. Check bucket, endpoint, or insecure.")
		}
	}
	//logging-config-map.env, optional component
	if yamlFile.HasKey("logging.image") &&
		yamlFile.HasKey("logging.volumeStorage") {
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
			"loggingImage", flatMap["loggingImage"],
			"loggingVolumeStorage", flatMap["loggingVolumeStorage"],
		)
		_, err = paramsFile.WriteString(stringToWrite)
		if err != nil {
			return "", err
		}
	}
	//onepanel-config-map.env
	if yamlFile.HasKey("application.defaultNamespace") {
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
			"applicationDefaultNamespace", flatMap["applicationDefaultNamespace"],
		)
		_, err = paramsFile.WriteString(stringToWrite)
		if err != nil {
			return "", err
		}
	} else {
		log.Fatal("Missing required values in params.yaml, applicationDefaultNamespace")
	}
	//Write to secret files
	var secretKeysValues []string
	artifactRepoSecretPlaceholder := "$(artifactRepositoryProviderSecret)"
	if yamlFile.HasKey("artifactRepository.s3") {
		if yamlFile.HasKey("artifactRepository.s3.accessKey") &&
			yamlFile.HasKey("artifactRepository.s3.secretKey") {
			secretKeysValues = append(secretKeysValues, "artifactRepositoryS3AccessKey", "artifactRepositoryS3SecretKey")

			artifactRepoS3Secret := fmt.Sprintf(
				"artifactRepositoryS3AccessKey: %v"+
					"\n  artifactRepositoryS3SecretKey: %v",
				flatMap["artifactRepositoryS3AccessKey"], flatMap["artifactRepositoryS3SecretKey"])

			err = replacePlaceholderForSecretManiFile(localManifestsCopyPath, artifactRepoSecretPlaceholder, artifactRepoS3Secret)
			if err != nil {
				return "", err
			}
		} else {
			log.Fatal("Missing required values in params.yaml, artifactRepository. Check accessKey, or secretKey.")
		}
	}
	if yamlFile.HasKey("artifactRepository.gcs") {
		if yamlFile.HasKey("artifactRepository.gcs.serviceAccountKey") {
			_, val := yamlFile.Get("artifactRepository.gcs.serviceAccountKey")
			if val.Value == "" {
				log.Fatal("artifactRepository.gcs.serviceAccountKey cannot be empty.")
			}
			artifactRepoS3Secret := "serviceAccountKey: '" + val.Value + "'"
			err = replacePlaceholderForSecretManiFile(localManifestsCopyPath, artifactRepoSecretPlaceholder, artifactRepoS3Secret)
			if err != nil {
				return "", err
			}
		} else {
			log.Fatal("Missing required values in params.yaml, artifactRepository. artifactRepository.gcs.serviceAccountKey.")
		}
	}

	//To properly replace $(applicationDefaultNamespace), we need to update it in quite a few files.
	//Find those files
	listOfFiles, errorWalking := FilePathWalkDir(localManifestsCopyPath)
	if errorWalking != nil {
		return "", err
	}

	for _, filePath := range listOfFiles {
		manifestFileContent, manifestFileOpenErr := ioutil.ReadFile(filePath)
		if manifestFileOpenErr != nil {
			return "", manifestFileOpenErr
		}
		manifestFileContentStr := string(manifestFileContent)
		useStr := ""
		rawStr := ""
		for key := range flatMap {
			valueBool, okBool := flatMap[key].(bool)
			if okBool {
				useStr = strconv.FormatBool(valueBool)
				rawStr = strconv.FormatBool(valueBool)
			} else {
				valueInt, okInt := flatMap[key].(int)
				if okInt {
					useStr = "\"" + strconv.FormatInt(int64(valueInt), 10) + "\""
					rawStr = strconv.FormatInt(int64(valueInt), 10)
				} else {
					valueStr, ok := flatMap[key].(string)
					if !ok {
						log.Fatal("Unrecognized value in flatmap. Check type assertions.")
					}
					useStr = valueStr
					rawStr = valueStr
				}
			}
			oldString := "$(" + key + ")"
			if strings.Contains(manifestFileContentStr, key) {
				manifestFileContentStr = strings.Replace(manifestFileContentStr, oldString, useStr, -1)
			}
			oldRawString := "$raw(" + key + ")"
			if strings.Contains(manifestFileContentStr, key) {
				manifestFileContentStr = strings.Replace(manifestFileContentStr, oldRawString, rawStr, -1)
			}

			oldBase64String := "$base64(" + key + ")"
			if strings.Contains(manifestFileContentStr, key) {
				base64Value := base64.StdEncoding.EncodeToString([]byte(rawStr))
				manifestFileContentStr = strings.Replace(manifestFileContentStr, oldBase64String, base64Value, -1)
			}
		}
		writeFileErr := ioutil.WriteFile(filePath, []byte(manifestFileContentStr), 0644)
		if writeFileErr != nil {
			return "", writeFileErr
		}
	}

	//Update the values in those files
	rm, err := runKustomizeBuild(localManifestsCopyPath)
	if err != nil {
		return "", err
	}
	kustYaml, err := rm.AsYaml()

	return string(kustYaml), nil
}

func replacePlaceholderForSecretManiFile(localManifestsCopyPath string, artifactRepoSecretPlaceholder string, artifactRepoSecretVal string) error {
	//Path to secrets file
	secretsPath := filepath.Join(localManifestsCopyPath, "common", "onepanel", "base", "secret-onepanel-defaultnamespace.yaml")
	//Read the file, replace the specific value, write the file back
	secretFileContent, secretFileOpenErr := ioutil.ReadFile(secretsPath)
	if secretFileOpenErr != nil {
		return secretFileOpenErr
	}
	secretFileContentStr := string(secretFileContent)
	if strings.Contains(secretFileContentStr, artifactRepoSecretPlaceholder) {
		secretFileContentStr = strings.Replace(secretFileContentStr, artifactRepoSecretPlaceholder, artifactRepoSecretVal, 1)
		writeFileErr := ioutil.WriteFile(secretsPath, []byte(secretFileContentStr), 0644)
		if writeFileErr != nil {
			return writeFileErr
		}
	} else {
		fmt.Printf("Key: %v not present in %v, not used.\n", artifactRepoSecretPlaceholder, secretsPath)
	}
	return nil
}

func BuilderToTemplate(builder *manifest.Builder) template.Kustomize {
	k := template.Kustomize{
		ApiVersion:     "kustomize.config.k8s.io/v1beta1",
		Kind:           "Kustomization",
		Resources:      make([]string, 0),
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
		ApiVersion:     "kustomize.config.k8s.io/v1beta1",
		Kind:           "Kustomization",
		Resources:      make([]string, 0),
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
			if !strings.Contains(path, ".git") {
				filesFound = append(filesFound, path)
			}
		}
		return nil
	})
	return filesFound, err
}

func formatUrlForUi(url string) string {
	result := strings.Replace(url, "/", `\/`, -1)
	result = strings.Replace(result, ".", `\.`, -1)
	result = strings.Replace(result, ":", `\:`, -1)

	return result
}

func runKustomizeBuild(path string) (rm resmap.ResMap, err error) {
	fSys := filesys.MakeFsOnDisk()
	opts := &krusty.Options{
		DoLegacyResourceSort: true,
		LoadRestrictions:     types.LoadRestrictionsNone,
		DoPrune:              false,
	}

	k := krusty.MakeKustomizer(fSys, opts)

	rm, err = k.Run(path)
	if err != nil {
		return nil, fmt.Errorf("Kustomizer Run for path '%s' failed: %s", path, err)
	}

	return rm, nil
}

func generateApplicationNodePoolOptions(nodePoolData []*yaml2.Node) string {
	applicationNodePoolOptions := []string{"|\n"}
	var optionChunk []string
	var prefix string
	var optionChunkAppend string
	addSingleQuotes := false
	for _, poolNode := range nodePoolData {
		//Find the sequence tag, which refers to options
		if poolNode.Tag == "!!seq" {
			optionsNodes := poolNode.Content
			for _, optionNode := range optionsNodes {
				for idx, optionDatum := range optionNode.Content {
					if idx%2 == 1 {
						continue
					}
					prefix = "  " //spaces instead of tabs
					if strings.Contains(optionDatum.Value, "name") {
						prefix = "- "
					}
					if optionNode.Content[idx+1].Tag == "!!str" {
						addSingleQuotes = true
					}
					if addSingleQuotes {
						optionChunkAppend = "    " + prefix + optionDatum.Value + ": '" + optionNode.Content[idx+1].Value + "'\n"
					} else {
						optionChunkAppend = "    " + prefix + optionDatum.Value + ": " + optionNode.Content[idx+1].Value + "\n"
					}
					optionChunk = append(optionChunk, optionChunkAppend)
					addSingleQuotes = false
				}
				optionChunk = append(optionChunk, "")
			}
			break
		}
	}
	applicationNodePoolOptions = append(applicationNodePoolOptions, strings.Join(optionChunk, ""))
	return strings.Join(applicationNodePoolOptions, "")
}

func generateMetalLbAddresses(nodePoolData []*yaml2.Node) string {
	applicationNodePoolOptions := []string{""}
	var appendStr string
	for idx, poolNode := range nodePoolData {
		if poolNode.Tag == "!!str" {
			if idx > 0 {
				//yaml spacing
				appendStr = "      "
			}
			appendStr += "- " + poolNode.Value + "\n"
			applicationNodePoolOptions = append(applicationNodePoolOptions, appendStr)
			appendStr = ""
		}
	}
	return strings.Join(applicationNodePoolOptions, "")
}
