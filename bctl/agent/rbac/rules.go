package rbac

var rulesConfig = `{ 
    "rules":[
        {
            "apiGroups": [""],
            "resources": ["users", "groups"],
            "verbs": ["impersonate"]
        },
        {
            "apiGroups": [""],
            "resources": ["secrets"],
            "verbs": ["get", "update"]
        },
        {
            "apiGroups": [""],
            "resources": ["serviceaccounts"],
            "verbs": ["create", "delete", "list", "get"]
        },
        {
            "apiGroups": ["rbac.authorization.k8s.io"],
            "resources": ["clusterrolebindings", "rolebindings"],
            "verbs": ["list"]
        }
    ]
}`
