#include <iostream>
#include <string>
#include <vector>
#include <optional>

namespace sample {

struct User {
    std::string name;
    int age;
};

class Repository {
public:
    explicit Repository(std::vector<User> users) : users_(std::move(users)) {}

    std::optional<User> find(const std::string &name) const {
        for (const auto &u : users_) {
            if (u.name == name) return u;
        }
        return std::nullopt;
    }

    std::size_t count() const { return users_.size(); }

private:
    std::vector<User> users_;
};

std::string greet(const User &u) {
    return "Hello, " + u.name + "!";
}

} // namespace sample

int main() {
    using namespace sample;
    Repository repo({{"alice", 30}, {"bob", 25}});
    if (auto u = repo.find("alice")) std::cout << greet(*u) << "\n";
    std::cout << "total: " << repo.count() << "\n";
    return 0;
}
