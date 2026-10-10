{ lib, buildPythonPackage, fetchPypi, anyio, genai-prices, griffelib, httpx
, logfire-api, opentelemetry-api, pydantic, typing-inspection }:

# nixpkgs' PydanticAI 2.52 needs newer OpenAI/Anthropic/Google SDKs than
# nixpkgs supplies. 2.31 works with nixpkgs' OpenAI and Google SDKs plus
# anthropic-compat.nix. Package these small wheels, sharing their dependencies.
let
  version = "2.31.0";
  graph = buildPythonPackage {
    pname = "pydantic-graph";
    inherit version;
    format = "wheel";
    src = fetchPypi {
      pname = "pydantic_graph";
      inherit version;
      format = "wheel";
      python = "py3";
      dist = "py3";
      hash = "sha256-BiVVyJsdVpndqqbwmuYvwdD1zBidCGfi+vymjCR5e6U=";
    };
    dependencies = [ anyio httpx logfire-api pydantic typing-inspection ];
    pythonImportsCheck = [ "pydantic_graph" ];
    meta.license = lib.licenses.mit;
  };
in buildPythonPackage {
  pname = "pydantic-ai-slim";
  inherit version;
  format = "wheel";
  src = fetchPypi {
    pname = "pydantic_ai_slim";
    inherit version;
    format = "wheel";
    python = "py3";
    dist = "py3";
    hash = "sha256-y4Ca2UnKaL5ruaDguZT8c6lfTNQF6GCacQNPLiCA4qE=";
  };
  dependencies = [
    anyio genai-prices griffelib httpx opentelemetry-api graph pydantic typing-inspection
  ];
  pythonImportsCheck = [ "pydantic_ai" ];
  meta = {
    description = "PydanticAI compatible with the pinned nixpkgs provider SDKs";
    homepage = "https://ai.pydantic.dev/";
    license = lib.licenses.mit;
  };
}
