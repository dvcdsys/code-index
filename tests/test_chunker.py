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
