import { commands, webview } from "mayoi";

commands.register("zut.open", async () => {
  await webview.open("zut.panel");
});
