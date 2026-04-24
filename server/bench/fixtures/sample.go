package sample

import "fmt"

type User struct {
	Name string
	Age  int
}

type Repository struct {
	users []User
}

func NewRepository(users []User) *Repository {
	return &Repository{users: users}
}

func (r *Repository) Find(name string) (*User, bool) {
	for i := range r.users {
		if r.users[i].Name == name {
			return &r.users[i], true
		}
	}
	return nil, false
}

func (r *Repository) Count() int {
	return len(r.users)
}

func Greet(u User) string {
	return fmt.Sprintf("Hello, %s!", u.Name)
}

func Run() {
	repo := NewRepository([]User{{Name: "alice", Age: 30}, {Name: "bob", Age: 25}})
	if u, ok := repo.Find("alice"); ok {
		fmt.Println(Greet(*u))
	}
	fmt.Printf("total: %d\n", repo.Count())
}
