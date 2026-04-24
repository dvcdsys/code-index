class User {
  constructor(name, age) {
    this.name = name;
    this.age = age;
  }
}

class Repository {
  constructor(users) {
    this.users = users;
  }

  find(name) {
    for (const u of this.users) {
      if (u.name === name) return u;
    }
    return null;
  }

  count() {
    return this.users.length;
  }
}

function greet(user) {
  return `Hello, ${user.name}!`;
}

const add = (a, b) => a + b;

function main() {
  const repo = new Repository([new User("alice", 30), new User("bob", 25)]);
  const found = repo.find("alice");
  if (found) console.log(greet(found));
  console.log(`total: ${repo.count()}, sum: ${add(1, 2)}`);
}

main();
