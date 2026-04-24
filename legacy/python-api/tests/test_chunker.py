"""Tests for the chunker service — runs locally without Docker."""
import sys
from pathlib import Path

# Add api directory to path for local testing
sys.path.insert(0, str(Path(__file__).parent.parent / "api"))

import pytest


def _make_chunker():
    """Create chunker service instance."""
    from app.services.chunker import ChunkerService
    return ChunkerService()


PYTHON_CODE = '''
import os
import sys

CONSTANT = 42

def hello(name: str) -> str:
    """Say hello."""
    return f"Hello, {name}!"

class Calculator:
    """A simple calculator."""

    def __init__(self, initial: int = 0):
        self.value = initial

    def add(self, n: int) -> int:
        self.value += n
        return self.value

    def subtract(self, n: int) -> int:
        self.value -= n
        return self.value

def main():
    calc = Calculator(10)
    print(hello("World"))
    print(calc.add(5))
'''

GO_CODE = '''package main

import "fmt"

type Server struct {
    host string
    port int
}

func NewServer(host string, port int) *Server {
    return &Server{host: host, port: port}
}

func (s *Server) Start() error {
    fmt.Printf("Starting on %s:%d\\n", s.host, s.port)
    return nil
}

func main() {
    s := NewServer("localhost", 8080)
    s.Start()
}
'''

PLAIN_TEXT = "Just some plain text that has no code structure at all. " * 20


class TestTreeSitterIntegration:
    """Verify tree-sitter bindings load correctly — catches version incompatibilities."""

    def test_parser_loads_for_all_language_nodes(self):
        """Every language in LANGUAGE_NODES must have a working parser (not None)."""
        from app.services.chunker import LANGUAGE_NODES
        chunker = _make_chunker()
        for language in LANGUAGE_NODES:
            parser = chunker._get_parser(language)
            assert parser is not None, (
                f"_get_parser('{language}') returned None — "
                f"tree-sitter binding broken or missing for '{language}'"
            )

    def test_parser_produces_ast(self):
        """Parser.parse() must return a tree with a root_node."""
        chunker = _make_chunker()
        parser = chunker._get_parser("python")
        assert parser is not None
        tree = parser.parse(b"def foo(): pass")
        assert tree.root_node is not None
        assert tree.root_node.type == "module"


class TestChunkerPython:
    def test_extracts_functions(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("test.py", PYTHON_CODE, "python")
        chunks = result.chunks
        func_chunks = [c for c in chunks if c.chunk_type == "function"]
        func_names = {c.symbol_name for c in func_chunks}
        assert "hello" in func_names
        assert "main" in func_names

    def test_extracts_class(self):
        chunker = _make_chunker()
        chunks = chunker.chunk_file("test.py", PYTHON_CODE, "python").chunks
        class_chunks = [c for c in chunks if c.chunk_type == "class"]
        assert any(c.symbol_name == "Calculator" for c in class_chunks)

    def test_extracts_methods(self):
        chunker = _make_chunker()
        chunks = chunker.chunk_file("test.py", PYTHON_CODE, "python").chunks
        method_chunks = [c for c in chunks if c.chunk_type == "method"]
        method_names = {c.symbol_name for c in method_chunks}
        assert "add" in method_names
        assert "__init__" in method_names

    def test_module_chunks(self):
        chunker = _make_chunker()
        chunks = chunker.chunk_file("test.py", PYTHON_CODE, "python").chunks
        module_chunks = [c for c in chunks if c.chunk_type == "module"]
        # Should capture imports and constants
        assert len(module_chunks) > 0

    def test_line_numbers(self):
        chunker = _make_chunker()
        chunks = chunker.chunk_file("test.py", PYTHON_CODE, "python").chunks
        for chunk in chunks:
            assert chunk.start_line >= 1
            assert chunk.end_line >= chunk.start_line


class TestChunkerGo:
    def test_extracts_functions(self):
        chunker = _make_chunker()
        chunks = chunker.chunk_file("main.go", GO_CODE, "go").chunks
        func_chunks = [c for c in chunks if c.chunk_type == "function"]
        func_names = {c.symbol_name for c in func_chunks}
        assert "NewServer" in func_names or "main" in func_names

    def test_extracts_type(self):
        chunker = _make_chunker()
        chunks = chunker.chunk_file("main.go", GO_CODE, "go").chunks
        type_chunks = [c for c in chunks if c.chunk_type == "type"]
        assert any(c.symbol_name == "Server" for c in type_chunks)


TYPESCRIPT_CODE = '''
import { Request, Response } from "express";

interface User {
    id: number;
    name: string;
}

type UserRole = "admin" | "user";

function getUser(id: number): User {
    return { id, name: "test" };
}

class UserService {
    private users: User[] = [];

    addUser(user: User): void {
        this.users.push(user);
    }
}

const fetchUser = (id: number): Promise<User> => {
    return Promise.resolve({ id, name: "test" });
};
'''

JAVASCRIPT_CODE = '''
const express = require("express");

function createApp() {
    const app = express();
    return app;
}

class Router {
    constructor() {
        this.routes = [];
    }

    addRoute(path, handler) {
        this.routes.push({ path, handler });
    }
}

const handler = (req, res) => {
    res.json({ ok: true });
};
'''

RUST_CODE = '''
use std::collections::HashMap;

struct Config {
    host: String,
    port: u16,
}

enum AppError {
    NotFound,
    Internal(String),
}

trait Handler {
    fn handle(&self, req: &str) -> Result<String, AppError>;
}

fn create_config() -> Config {
    Config { host: "localhost".to_string(), port: 8080 }
}
'''

JAVA_CODE = '''
package com.example;

import java.util.List;

interface Repository {
    List<String> findAll();
}

class UserService {
    private final Repository repo;

    UserService(Repository repo) {
        this.repo = repo;
    }

    public List<String> getUsers() {
        return repo.findAll();
    }
}
'''

LUA_CODE = '''
local M = {}

function M.setup(opts)
    opts = opts or {}
    M.debug = opts.debug or false
end

function M.greet(name)
    return "Hello, " .. name
end

return M
'''

YAML_CODE = '''
name: CI Pipeline
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make test
'''

JSON_CODE = '''
{
    "name": "my-project",
    "version": "1.0.0",
    "dependencies": {
        "express": "^4.18.0"
    },
    "scripts": {
        "start": "node index.js",
        "test": "jest"
    }
}
'''


class TestChunkerMultiLanguage:
    """Verify tree-sitter parses all LANGUAGE_NODES languages (not falling back to sliding window)."""

    @pytest.mark.parametrize("filename,code,language,expected_symbols", [
        ("test.py", PYTHON_CODE, "python", {"hello", "Calculator"}),
        ("test.ts", TYPESCRIPT_CODE, "typescript", {"getUser", "UserService"}),
        ("test.js", JAVASCRIPT_CODE, "javascript", {"createApp", "Router"}),
        ("main.go", GO_CODE, "go", {"NewServer", "Server"}),
        ("lib.rs", RUST_CODE, "rust", {"Config", "Handler", "create_config"}),
        ("Main.java", JAVA_CODE, "java", {"UserService", "Repository"}),
    ])
    def test_treesitter_parses_language(self, filename, code, language, expected_symbols):
        chunker = _make_chunker()
        result = chunker.chunk_file(filename, code, language)
        structured_types = {"function", "class", "method", "type"}
        structured = [c for c in result.chunks if c.chunk_type in structured_types]
        assert len(structured) > 0, f"{language}: fell back to sliding window, no structured chunks"
        found_names = {c.symbol_name for c in structured if c.symbol_name}
        for sym in expected_symbols:
            assert sym in found_names, f"{language}: expected symbol '{sym}' not found in {found_names}"

    @pytest.mark.parametrize("filename,code,language", [
        ("test.py", PYTHON_CODE, "python"),
        ("test.ts", TYPESCRIPT_CODE, "typescript"),
        ("test.js", JAVASCRIPT_CODE, "javascript"),
        ("main.go", GO_CODE, "go"),
        ("lib.rs", RUST_CODE, "rust"),
        ("Main.java", JAVA_CODE, "java"),
    ])
    def test_references_extracted(self, filename, code, language):
        chunker = _make_chunker()
        result = chunker.chunk_file(filename, code, language)
        assert len(result.references) > 0, f"{language}: no references extracted"
        for ref in result.references:
            assert ref.file_path == filename
            assert ref.line >= 1
            assert ref.language == language

    @pytest.mark.parametrize("filename,code,language", [
        ("script.lua", LUA_CODE, "lua"),
        ("config.yaml", YAML_CODE, "yaml"),
        ("package.json", JSON_CODE, "json"),
    ])
    def test_no_crash_on_data_languages(self, filename, code, language):
        """Languages without LANGUAGE_NODES fall back to sliding window without errors."""
        chunker = _make_chunker()
        result = chunker.chunk_file(filename, code, language)
        assert len(result.chunks) > 0, f"{language}: produced no chunks at all"
        assert all(c.chunk_type == "block" for c in result.chunks), (
            f"{language}: expected sliding-window blocks"
        )


class TestChunkerFallback:
    def test_sliding_window(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("readme.txt", PLAIN_TEXT, "text")
        assert len(result.chunks) > 0
        assert all(c.chunk_type == "block" for c in result.chunks)
        assert result.references == []

    def test_empty_file(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("empty.py", "", "python")
        assert len(result.chunks) == 0


class TestReferenceExtraction:
    def test_extracts_references_python(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("test.py", PYTHON_CODE, "python")
        ref_names = {r.name for r in result.references}
        # Calculator and hello are used in main()
        assert "Calculator" in ref_names
        assert "hello" in ref_names

    def test_skips_definition_names(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("test.py", PYTHON_CODE, "python")
        # "hello" should appear as reference (in main), but not at def line
        hello_refs = [r for r in result.references if r.name == "hello"]
        # The definition is at line 7 (def hello(...)), refs should not be there
        hello_def_line = None
        for c in result.chunks:
            if c.symbol_name == "hello" and c.chunk_type == "function":
                hello_def_line = c.start_line
                break
        assert hello_def_line is not None
        assert all(r.line != hello_def_line for r in hello_refs)

    def test_skips_keywords(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("test.py", PYTHON_CODE, "python")
        ref_names = {r.name for r in result.references}
        assert "self" not in ref_names
        assert "None" not in ref_names
        assert "True" not in ref_names

    def test_refs_have_correct_file_path(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("test.py", PYTHON_CODE, "python")
        for ref in result.references:
            assert ref.file_path == "test.py"
            assert ref.line >= 1
            assert ref.col >= 0
            assert ref.language == "python"

    def test_extracts_references_go(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("main.go", GO_CODE, "go")
        ref_names = {r.name for r in result.references}
        # NewServer and Start are used in main()
        assert "NewServer" in ref_names or "Server" in ref_names

    def test_no_refs_for_unsupported_language(self):
        chunker = _make_chunker()
        result = chunker.chunk_file("readme.txt", PLAIN_TEXT, "text")
        assert result.references == []
