"use strict";
(async () => {
  try {
    const [state, profiles, models, choices] = await Promise.all([api("state"), api("profiles"), api("trained-models"),api("asr/models").catch(()=>null)]);
    if(choices)asrOptions=choices;
    $("engine-open").onclick=()=>$("engine-dialog").showModal();
    draft = state.draft;
    studioBuild = state.build || null;
    asrChoice=asrOptions.some(x=>x.id===draft.asrModel&&x.status==="ready")?draft.asrModel:"";
    engines = state.engines;
    for(const k of Object.keys(engines))if(!catalog[k]){
      const meta=engines[k];catalog[k]=[meta.title||k,meta.description||"本機語音合成引擎。"];
    }
    voices = profiles;
    trainedModels = models;
    if (draft.voiceId && !selectedVoice()) delete draft.voiceId;
    draft.drafts ||= {};
    if (!catalog[draft.engine]) draft.engine = "gpt-sovits";
    restoreWorkspace();
    setWorkspaceFlags();
    mountShell();
    document.addEventListener("visibilitychange", () => {
      if (document.hidden && $("saved").textContent === "正在儲存草稿…")
        persist();
    });
    render();
    poll();
  } catch (e) {
    notice(e);
  }
})();
