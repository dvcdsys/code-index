"""Sample module for tree-sitter parsing test."""

from dataclasses import dataclass


@dataclass
class User:
    name: str
    age: int


def greet(user: User) -> str:
    return f"Hello, {user.name}!"


class Repository:
    def __init__(self, users: list[User]) -> None:
        self._users = users

    def find(self, name: str) -> User | None:
        for u in self._users:
            if u.name == name:
                return u
        return None

    def count(self) -> int:
        return len(self._users)


def main() -> None:
    repo = Repository([User("alice", 30), User("bob", 25)])
    found = repo.find("alice")
    if found:
        print(greet(found))
    print(f"total: {repo.count()}")


if __name__ == "__main__":
    main()
