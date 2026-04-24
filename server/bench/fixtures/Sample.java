package sample;

import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

interface Greeter {
    String greet(String name);
}

public class Sample implements Greeter {
    private final List<String> users;

    public Sample() {
        this.users = new ArrayList<>();
    }

    public void add(String user) {
        users.add(user);
    }

    public Optional<String> find(String name) {
        for (String u : users) {
            if (u.equals(name)) return Optional.of(u);
        }
        return Optional.empty();
    }

    public int count() {
        return users.size();
    }

    @Override
    public String greet(String name) {
        return "Hello, " + name + "!";
    }

    public static void main(String[] args) {
        Sample s = new Sample();
        s.add("alice");
        s.add("bob");
        s.find("alice").ifPresent(u -> System.out.println(s.greet(u)));
        System.out.println("total: " + s.count());
    }
}
