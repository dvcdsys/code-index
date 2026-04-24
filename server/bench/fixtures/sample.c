#include <stdio.h>
#include <string.h>
#include <stdlib.h>

typedef struct {
    char name[64];
    int age;
} User;

typedef struct {
    User *users;
    int count;
} Repository;

Repository *repo_new(int capacity) {
    Repository *r = malloc(sizeof(Repository));
    r->users = calloc(capacity, sizeof(User));
    r->count = 0;
    return r;
}

void repo_add(Repository *r, const char *name, int age) {
    User *u = &r->users[r->count++];
    strncpy(u->name, name, sizeof(u->name) - 1);
    u->age = age;
}

User *repo_find(Repository *r, const char *name) {
    for (int i = 0; i < r->count; i++) {
        if (strcmp(r->users[i].name, name) == 0) return &r->users[i];
    }
    return NULL;
}

void repo_free(Repository *r) {
    free(r->users);
    free(r);
}

int main(void) {
    Repository *r = repo_new(8);
    repo_add(r, "alice", 30);
    repo_add(r, "bob", 25);
    User *u = repo_find(r, "alice");
    if (u) printf("Hello, %s!\n", u->name);
    printf("total: %d\n", r->count);
    repo_free(r);
    return 0;
}
