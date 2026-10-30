package webish

import "example.test/fix/internal/authz"

func viaNew() { _ = new(authz.Subject) }
