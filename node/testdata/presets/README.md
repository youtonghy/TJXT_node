Node configs of TONG-ladder's recommended presets, exactly as the panel
delivers them (keys are throwaway samples). Regenerate them from the panel
repository when the presets or the node contract change:

    TJXT_EXPORT_NODE_PRESETS=<TJXT_node>/node/testdata/presets \
      cargo test -p tjxt-server --test node every_preset

`TestPanelPresetsStartXray` starts a real Xray inbound with each of them;
run it on every Xray upgrade.
