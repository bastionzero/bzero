package rbac

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"bastionzero.com/bctl/v1/bzerolib/logger"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type rule struct {
	ApiGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

type requirements struct {
	Rules []rule `json:"rules"`
}

func loadConfig() (requirements, error) {
	var parsedConfig requirements
	if err := json.Unmarshal([]byte(rulesConfig), &parsedConfig); err != nil {
		return parsedConfig, err
	}
	return parsedConfig, nil
}

func createMissing(reqs requirements) map[string]map[string]map[string]bool {
	// create a dictionary so we can be specific about the permissions they're missing
	missing := make(map[string]map[string]map[string]bool)
	for _, ruleReq := range reqs.Rules {
		for _, apiGroupReq := range ruleReq.ApiGroups {
			if _, ok := missing[apiGroupReq]; !ok {
				missing[apiGroupReq] = make(map[string]map[string]bool)
			}
			for _, resourceReq := range ruleReq.Resources {
				if _, ok := missing[apiGroupReq][resourceReq]; !ok {
					missing[apiGroupReq][resourceReq] = make(map[string]bool)
				}
				for _, verbReq := range ruleReq.Verbs {
					if _, ok := missing[apiGroupReq][resourceReq][verbReq]; !ok {
						missing[apiGroupReq][resourceReq][verbReq] = false
					}
				}
			}
		}
	}
	return missing
}

func prettyPrintMissing(missing map[string]map[string]map[string]bool) []rule {
	// put it back into a permissions rule format so it's easier for users to understand
	prettyPrint := []rule{}
	for apiGroup, resources := range missing {
		for resource, verbs := range resources {
			for verb := range verbs {
				prettyPrint = append(prettyPrint, rule{
					ApiGroups: []string{apiGroup},
					Resources: []string{resource},
					Verbs:     []string{verb},
				})
			}
		}
	}
	return prettyPrint
}

func CheckPermissions(logger *logger.Logger, namespace string) error {
	// verify the current namespace matches what was passed in
	if data, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 && ns != namespace {
			return fmt.Errorf("current namespace %s does not match expected %s", ns, namespace)
		}
	}

	// check for correct service account permissions
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return err
	}

	sar := &authorizationv1.SelfSubjectRulesReview{
		Spec: authorizationv1.SelfSubjectRulesReviewSpec{
			Namespace: namespace,
		},
	}

	// all rules as provided by the system
	rules, rerr := clientset.AuthorizationV1().SelfSubjectRulesReviews().Create(context.TODO(), sar, metav1.CreateOptions{})
	if rerr != nil {
		return fmt.Errorf("could not review service account permissions: %s", rerr)
	}

	// all required rules
	reqs, err := loadConfig()
	if err != nil {
		return err
	}

	// create a dictionary so we can be specific about the permissions they're missing
	missing := createMissing(reqs)

	// iterate through the list of provided rules and requirements, and delete from the dictionary of missing permissions.
	for _, rule := range rules.Status.ResourceRules {
		for _, apiGroup := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					for _, ruleReq := range reqs.Rules {
						for _, apiGroupReq := range ruleReq.ApiGroups {
							if apiGroup == apiGroupReq {
								for _, resourceReq := range ruleReq.Resources {
									if resource == resourceReq {
										for _, verbReq := range ruleReq.Verbs {
											if verb == verbReq {
												if _, ok := missing[apiGroup][resource][verb]; ok {
													delete(missing[apiGroup][resource], verb)

													// cleanup apigroups and resources as we go
													if len(missing[apiGroup][resource]) == 0 {
														delete(missing[apiGroup], resource)
														if len(missing[apiGroup]) == 0 {
															delete(missing, apiGroup)
														}
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	if len(missing) == 0 {
		return nil
	} else {
		// put it back into a permissions rule format so it's easier for users to understand
		prettyPrint := prettyPrintMissing(missing)

		return fmt.Errorf("service account lacks sufficient permissions, missing: %+v", prettyPrint)
	}
}
