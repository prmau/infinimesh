//--------------------------------------------------------------------------
// Copyright 2018 Infinite Devices GmbH
// www.infinimesh.io
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.
//--------------------------------------------------------------------------

package dgraph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/dgraph-io/dgo"
	"github.com/dgraph-io/dgo/protos/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/infinimesh/infinimesh/pkg/node"
	"github.com/infinimesh/infinimesh/pkg/node/nodepb"
)

func isPermissionSufficient(required, actual string) bool {
	switch required {
	case "WRITE":
		return actual == "WRITE"
	case "READ":
		return actual == "WRITE" || actual == "READ"
	default:
		return false
	}
}

type DGraphRepo struct {
	Dg *dgo.Dgraph
}

func NewDGraphRepo(dg *dgo.Dgraph) node.Repo {
	return &DGraphRepo{Dg: dg}
}

func checkType(ctx context.Context, txn *dgo.Txn, uid, _type string) bool {
	q := `query object($_uid: string, $type: string) {
                object(func: uid($_uid)) @filter(eq(type, $type)) {
                  uid
                }
              }
             `
	resp, err := txn.QueryWithVars(ctx, q, map[string]string{
		"$_uid": uid,
		"$type": _type,
	})
	if err != nil {
		return false
	}

	var result struct {
		Object []map[string]interface{} `json:"object"`
	}

	err = json.Unmarshal(resp.Json, &result)
	if err != nil {
		return false
	}

	return len(result.Object) > 0
}

func NameExists(ctx context.Context, txn *dgo.Txn, name, namespace, parent string) bool { //nolint
	var q string
	if parent == "" {
		q = `query object($name: string, $namespace: string, $parent: uid){
  object(func: eq(name, $name)) @cascade {
    uid
    name
    ~owns @filter(eq(name, $namespace)) {
      name
    }
  }
}
`
	} else {
		q = `query exists($name: string, $namespace: string, $parent: uid){
  exists(func: eq(name, $name)) @cascade {
    uid
    name
    ~owns @filter(eq(name, $namespace)) {
      name
    }
    ~children @filter(uid($parent)) {
      uid
      name
    }
  }
}
`

	}

	resp, err := txn.QueryWithVars(ctx, q, map[string]string{
		"$parent":    parent,
		"$name":      name,
		"$namespace": namespace,
	})
	if err != nil {
		return false
	}

	var result struct {
		Object []map[string]interface{} `json:"object"`
	}

	err = json.Unmarshal(resp.Json, &result)
	if err != nil {
		return false
	}

	return len(result.Object) > 0
}

func FingerprintExists(ctx context.Context, txn *dgo.Txn, fingerprint []byte) bool { //nolint
	q := `query devices($fingerprint: string){
		devices(func: eq(fingerprint, $fingerprint)) @normalize {
		  ~certificates {
			uid : uid
			name : name
			enabled : enabled
			~owns {
			  namespace: name
			}
		  }
		}
	  }
		`

	vars := map[string]string{
		"$fingerprint": base64.StdEncoding.EncodeToString(fingerprint),
	}
	resp, err := txn.QueryWithVars(ctx, q, vars)
	if err != nil {
		return false
	}

	var result struct {
		Devices []struct {
			UID       string `json:"uid"`
			Name      string `json:"name"`
			Enabled   bool   `json:"enabled"`
			Namespace string `json:"namespace"`
		} `json:"devices"`
	}

	err = json.Unmarshal(resp.Json, &result)
	if err != nil {
		return false
	}

	return len(result.Devices) > 0
}

func CheckExists(ctx context.Context, txn *dgo.Txn, uid string) bool { //nolint
	q := `query object($_uid: string) {
                object(func: uid($_uid)) {
                  uid
                }
              }
             `
	resp, err := txn.QueryWithVars(ctx, q, map[string]string{
		"$_uid": uid,
	})
	if err != nil {
		return false
	}

	var result struct {
		Object []map[string]interface{} `json:"object"`
	}

	err = json.Unmarshal(resp.Json, &result)
	if err != nil {
		return false
	}

	return len(result.Object) > 0
}

func (s *DGraphRepo) AuthorizeNamespace(ctx context.Context, account, namespace string, action nodepb.Action) (err error) {
	txn := s.Dg.NewTxn()

	if ok := checkType(ctx, txn, account, "account"); !ok {
		return errors.New("invalid account")
	}

	// TODO use internal method that runs within txn
	ns, err := s.GetNamespace(ctx, namespace)
	if err != nil {
		return err
	}

	_, err = txn.Mutate(ctx, &api.Mutation{
		Set: []*api.NQuad{
			&api.NQuad{
				Subject:   account,
				Predicate: "access.to.namespace",
				ObjectId:  ns.Id,
				Facets: []*api.Facet{
					&api.Facet{
						Key:     "permission",
						Value:   []byte(action.String()),
						ValType: api.Facet_STRING,
					},
				},
			},
		},
		CommitNow: true,
	})
	if err != nil {
		return errors.New("Failed to mutate")
	}
	return nil

}

func (s *DGraphRepo) Authenticate(ctx context.Context, username, password string) (success bool, uid string, defaultNamespace string, err error) {
	txn := s.Dg.NewReadOnlyTxn()

	const q = `query authenticate($username: string, $password: string){
  login(func: eq(username, $username)) @filter(eq(type, "credentials")) {
    uid
    checkpwd(password, $password)
    ~has.credentials {
      uid
      type
      enabled
      default.namespace{
        uid
        name
      }
    }
  }
}
`

	resp, err := txn.QueryWithVars(ctx, q, map[string]string{"$username": username, "$password": password})
	if err != nil {
		return false, "", "", err
	}

	var result struct {
		Login []*UsernameCredential `json:"login"`
	}

	err = json.Unmarshal(resp.Json, &result)
	if err != nil {
		return false, "", "", err
	}

	if len(result.Login) > 0 {
		login := result.Login[0]
		if login.CheckPwd {
			// Success
			if len(login.Account) > 0 {
				if !login.Account[0].Enabled {
					return false, "", "", status.Error(codes.Unauthenticated, "Account is disabled")
				}
				if len(login.Account[0].DefaultNamespace) > 0 {
					defaultNamespace = login.Account[0].DefaultNamespace[0].Name
				}
				return result.Login[0].CheckPwd, login.Account[0].UID, defaultNamespace, nil
			}
		}
	}
	return false, "", "", errors.New("Invalid credentials")
}

func (s *DGraphRepo) SetPassword(ctx context.Context, account, password string) error {
	txn := s.Dg.NewTxn()
	const q = `query accounts($account: string) {
                     accounts(func: eq(name, $account)) @filter(eq(type, "account"))  {
                       uid
                       has.credentials {
                         name @filter(eq(username, $account))
                         uid
                       }
                     }
                   }`

	response, err := txn.QueryWithVars(ctx, q, map[string]string{"$account": account})
	if err != nil {
		return err
	}

	var result struct {
		Account []*Account `json:"accounts"`
	}

	err = json.Unmarshal(response.Json, &result)
	if err != nil {
		return err
	}

	if len(result.Account) == 0 {
		return errors.New("Account not found")
	}

	if len(result.Account[0].HasCredentials) == 0 {
		return errors.New("No credentials found")
	}

	_, err = txn.Mutate(ctx, &api.Mutation{
		Set: []*api.NQuad{
			{
				Subject:     result.Account[0].HasCredentials[0].UID,
				Predicate:   "password",
				ObjectValue: &api.Value{Val: &api.Value_StrVal{StrVal: password}},
			},
		},
		CommitNow: true,
	})

	return err

}

func (s *DGraphRepo) Authorize(ctx context.Context, account, node, action string, inherit bool) (err error) {
	txn := s.Dg.NewTxn()

	if ok := checkType(ctx, txn, account, "account"); !ok {
		return errors.New("invalid account")
	}

	var _type string
	if ok := checkType(ctx, txn, node, "namespace"); !ok {
		if ok := checkType(ctx, txn, node, "object"); !ok {
			return errors.New("resource does not exist")
		} else {
			_type = "object"
		}
	} else {
		_type = "namespace"
	}

	var nq []*api.NQuad
	if _type == "namespace" {
		nq = append(nq,
			&api.NQuad{
				Subject:   account,
				Predicate: "access.to.namespace",
				ObjectId:  node,
				Facets: []*api.Facet{
					&api.Facet{
						Key:     "permission",
						Value:   []byte(action),
						ValType: api.Facet_STRING,
					},
				},
			},
		)
	} else if _type == "object" {
		nq = append(nq,
			&api.NQuad{
				Subject:   account,
				Predicate: "access.to",
				ObjectId:  node,
				Facets: []*api.Facet{
					&api.Facet{
						Key:     "permission",
						Value:   []byte(action),
						ValType: api.Facet_STRING,
					},
					&api.Facet{
						Key:     "inherit",
						Value:   []byte("true"),
						ValType: api.Facet_BOOL,
					},
				},
			},
		)
	}

	_, err = txn.Mutate(ctx, &api.Mutation{
		Set:       nq,
		CommitNow: true,
	})
	if err != nil {
		return errors.New("Failed to mutate")
	}
	return nil
}

func (s *DGraphRepo) CreateNamespace(ctx context.Context, name string) (id string, err error) {
	ns := &Namespace{
		Node: Node{
			Type: "namespace",
			UID:  "_:namespace",
		},
		Name: name,
	}

	txn := s.Dg.NewTxn()
	js, err := json.Marshal(&ns)
	if err != nil {
		return "", err
	}

	assigned, err := txn.Mutate(ctx, &api.Mutation{
		SetJson:   js,
		CommitNow: true,
	})
	if err != nil {
		return "", err
	}

	return assigned.GetUids()["namespace"], nil
}

func (s *DGraphRepo) GetNamespace(ctx context.Context, namespaceID string) (namespace *nodepb.Namespace, err error) {
	const q = `query getNamespaces($namespace: string) {
                     namespaces(func: eq(name, $namespace)) @filter(eq(type, "namespace"))  {
	               uid
                       name
	             }
                   }`

	res, err := s.Dg.NewReadOnlyTxn().QueryWithVars(ctx, q, map[string]string{"$namespace": namespaceID})
	if err != nil {
		return nil, err
	}

	var resultSet struct {
		Namespaces []*Namespace `json:"namespaces"`
	}

	if err := json.Unmarshal(res.Json, &resultSet); err != nil {
		return nil, err
	}

	if len(resultSet.Namespaces) > 0 {
		return &nodepb.Namespace{
			Id:   resultSet.Namespaces[0].UID,
			Name: resultSet.Namespaces[0].Name,
		}, nil
	}

	return nil, errors.New("Namespace not found")
}

func (s *DGraphRepo) IsAuthorized(ctx context.Context, node, account, action string) (decision bool, err error) {
	if node == account {
		return true, nil
	}

	params := map[string]string{
		"$device_id": node,
		"$user_id":   account,
	}

	txn := s.Dg.NewReadOnlyTxn()

	const qDirect = `query direct_access($device_id: string, $user_id: string){
                         direct(func: uid($user_id)) @normalize @cascade {
                           access.to  @filter(uid($device_id)) @facets(permission,inherit) {
                             type: type
                           }
                         }
                         direct_via_one_object(func: uid($user_id)) @normalize @cascade {
                           access.to @facets(permission,inherit) {
                             contains @filter(uid($device_id)) {
                               uid
                               type: type
                             }
                           }
                         }
                        }`

	res, err := txn.QueryWithVars(ctx, qDirect, params)
	if err != nil {
		return false, err
	}

	var permissions struct {
		Direct          []Object `json:"direct"`
		DirectViaObject []Object `json:"direct_via_one_object"`
	}

	err = json.Unmarshal(res.Json, &permissions)
	if err != nil {
		return false, err
	}

	if len(permissions.Direct) > 0 {
		if isPermissionSufficient(action, permissions.Direct[0].AccessToPermission) {
			return true, nil
		}
	}

	if len(permissions.DirectViaObject) > 0 {
		if isPermissionSufficient(action, permissions.DirectViaObject[0].AccessToPermission) {
			return true, nil
		}
	}

	const qRecursiveWrite = `query recursive($user_id: string, $device_id: string){
                         shortest(from: $user_id, to: $device_id) {
                           access.to @facets(eq(inherit, true) AND eq(permission,"WRITE"))
                           access.to.namespace @facets(eq(permission,"WRITE"))
                           owns
                           children
                         }
                       }`

	const qRecursiveRead = `query recursive($user_id: string, $device_id: string){
                         shortest(from: $user_id, to: $device_id) {
                           access.to @facets(eq(inherit, true) AND (eq(permission,"WRITE") OR eq(permission, "READ")))
                           access.to.namespace @facets((eq(permission,"WRITE") OR eq(permission, "READ")))
                           owns
                           children
                         }
                       }`

	var qRecursive string

	switch action {
	case "READ":
		qRecursive = qRecursiveRead
	case "WRITE":
		qRecursive = qRecursiveWrite
	default:
		return false, errors.New("Invalid action")
	}

	res, err = txn.QueryWithVars(ctx, qRecursive, params)
	if err != nil {
		return false, err
	}

	var rez struct {
		Path []map[string]interface{} `json:"_path_"`
	}

	if err = json.Unmarshal(res.Json, &rez); err != nil {
		return false, err
	}

	if len(rez.Path) > 0 {
		return true, nil
	}

	return false, nil
}
