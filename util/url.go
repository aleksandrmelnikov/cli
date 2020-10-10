package util

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
)

func GetWildCardDNS(url string) string {
	url = strings.ReplaceAll(url, "/", "")
	parts := strings.Split(url, ".")
	url = strings.Join(parts[1:], ".")

	return fmt.Sprintf("*.%v", url)
}

// GetFQDN figures out the sub-domain.domain value
func GetFQDN(yamlFile *DynamicYaml) (string, error) {
	fqdn := ""
	if yamlFile.GetValue("application.fqdn") != nil {
		fqdn = yamlFile.GetValue("application.fqdn").Value
	}
	if yamlFile.GetValue("application.name") != nil {
		subDomain := yamlFile.GetValue("application.name").Value
		if yamlFile.GetValue("application.domain") == nil {
			return "", errors.New("application.domain empty")
		}
		domain := yamlFile.GetValue("application.domain").Value
		fqdn = subDomain + "." + domain
		yamlFile.Put("application.fqdn", fqdn)
	}
	if fqdn == "" {
		return "", errors.New("application.name empty")
	}
	return fqdn, nil
}

func GetDeployedWebURL(yamlFile *DynamicYaml) (string, error) {
	httpScheme := "http://"
	fqdn, err := GetFQDN(yamlFile)
	if err != nil {
		return "", err
	}
	fqdnExtra := ""

	insecure, err := strconv.ParseBool(yamlFile.GetValue("application.insecure").Value)
	if err != nil {
		log.Fatal("insecure is not a bool")
	}

	if !insecure {
		httpScheme = "https://"
	}

	return fmt.Sprintf("%v%v%v", httpScheme, fqdn, fqdnExtra), nil
}
