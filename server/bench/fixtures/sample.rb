module Sample
  class User
    attr_reader :name, :age

    def initialize(name, age)
      @name = name
      @age = age
    end
  end

  class Repository
    def initialize(users)
      @users = users
    end

    def find(name)
      @users.find { |u| u.name == name }
    end

    def count
      @users.length
    end
  end

  def self.greet(user)
    "Hello, #{user.name}!"
  end
end

repo = Sample::Repository.new([
  Sample::User.new("alice", 30),
  Sample::User.new("bob", 25),
])

found = repo.find("alice")
puts Sample.greet(found) if found
puts "total: #{repo.count}"
