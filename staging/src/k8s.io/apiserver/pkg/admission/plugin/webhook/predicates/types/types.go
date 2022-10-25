package types

import "k8s.io/apimachinery/pkg/labels"

type NamespaceSelectorProvider interface {
	// GetNamespaceSelector gets the webhook NamespaceSelector field.
	GetParsedNamespaceSelector() (labels.Selector, error)
}

type ObjectSelectorProvider interface {
	// GetObjectSelector gets the webhook ObjectSelector field.
	GetParsedObjectSelector() (labels.Selector, error)
}