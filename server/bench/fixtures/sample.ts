interface IUser {
  name: string;
  age: number;
}

type UserList = IUser[];

class User implements IUser {
  constructor(public name: string, public age: number) {}
}

class Repository {
  private users: UserList;

  constructor(users: UserList) {
    this.users = users;
  }

  find(name: string): IUser | null {
    for (const u of this.users) {
      if (u.name === name) return u;
    }
    return null;
  }

  count(): number {
    return this.users.length;
  }
}

function greet(u: IUser): string {
  return `Hello, ${u.name}!`;
}

const add = (a: number, b: number): number => a + b;

function main(): void {
  const repo = new Repository([new User("alice", 30), new User("bob", 25)]);
  const found = repo.find("alice");
  if (found) console.log(greet(found));
  console.log(`total: ${repo.count()}, sum: ${add(1, 2)}`);
}

main();
