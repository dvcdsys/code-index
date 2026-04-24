use std::collections::HashMap;

trait Greeter {
    fn greet(&self, name: &str) -> String;
}

#[derive(Debug, Clone)]
pub struct User {
    pub name: String,
    pub age: u32,
}

pub enum Role {
    Admin,
    User,
    Guest,
}

pub struct Repository {
    users: HashMap<String, User>,
}

impl Repository {
    pub fn new() -> Self {
        Self { users: HashMap::new() }
    }

    pub fn add(&mut self, u: User) {
        self.users.insert(u.name.clone(), u);
    }

    pub fn find(&self, name: &str) -> Option<&User> {
        self.users.get(name)
    }

    pub fn count(&self) -> usize {
        self.users.len()
    }
}

impl Greeter for Repository {
    fn greet(&self, name: &str) -> String {
        format!("Hello, {}!", name)
    }
}

fn main() {
    let mut repo = Repository::new();
    repo.add(User { name: "alice".to_string(), age: 30 });
    repo.add(User { name: "bob".to_string(), age: 25 });
    if let Some(u) = repo.find("alice") {
        println!("{}", repo.greet(&u.name));
    }
    println!("total: {}", repo.count());
}
