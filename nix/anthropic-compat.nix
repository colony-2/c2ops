{ lib, buildPythonPackage, fetchPypi, anyio, distro, docstring-parser, httpx
, jiter, pydantic, sniffio, typing-extensions }:

# PydanticAI 2.31 passes httpx clients. nixpkgs' Anthropic 1.6 requires
# httpx2 clients, so retain the compatible SDK while sharing its dependencies.
buildPythonPackage rec {
  pname = "anthropic";
  version = "0.108.0";
  format = "wheel";
  src = fetchPypi {
    inherit pname version format;
    python = "py3";
    dist = "py3";
    hash = "sha256-ve57FME89aYLLIrgzxlXIODqf9irkN9aOJnFDxyRxL4=";
  };
  dependencies = [
    anyio distro docstring-parser httpx jiter pydantic sniffio typing-extensions
  ];
  pythonImportsCheck = [ "anthropic" ];
  meta = {
    description = "Anthropic SDK compatible with PydanticAI's httpx clients";
    homepage = "https://github.com/anthropics/anthropic-sdk-python";
    license = lib.licenses.mit;
  };
}
