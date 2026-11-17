cask "mayoi" do
  arch arm: "arm64", intel: "amd64"

  version "0.22.1"
  sha256 arm:   "55e4a53c19d6497f448ee2cff14e49e806e6a8805e68043df0e7199b3793bf31",
         intel: "4e5702161aef0c63e0864c9b284dcf8aedb74e62d140d7a560120dfd1780ce74"

  url "https://github.com/sorrycc/Zut/releases/download/v#{version}/mayoi-#{version}-#{arch}.dmg"
  name "Zut"
  desc "Small, fast code editor"
  homepage "https://github.com/sorrycc/Zut"

  auto_updates true
  depends_on macos: :ventura

  app "Zut.app"
  command_wrapper "mayoi", content: <<~SH
    #!/bin/sh
    # Managed by Homebrew; use brew uninstall --cask mayoi to remove it.
    app=#{(appdir/"Zut.app/Contents/MacOS/Zut").to_s.shellescape}
    [ -x "$app" ] || { echo "mayoi: Zut.app is not found" >&2; exit 1; }
    MAYOI_CLI=1 exec "$app" "$@"
  SH
end
